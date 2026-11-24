package server

import (
	"io"
	"log/slog"
	"net"

	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Behind a path-prefix reverse proxy the shells must carry the prefix
// in their <base>, and the session cookie must be scoped to it — but
// only when the prefix header comes from a trusted proxy.
func TestPrefixFromTrustedProxy(t *testing.T) {
	ui := fstest.MapFS{
		"index.html": {Data: []byte("<html><head><title>x</title></head><body>app</body></html>")},
	}
	e := newEnv(t, testBind, nil)
	srv := New(e.app, Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Web: ui,
		TrustedProxy: func(ip net.IP) bool {
			return ip.IsLoopback()
		},
	})
	defer srv.Close()
	h := srv.Handler()

	get := func(remoteAddr, prefix string) string {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remoteAddr
		if prefix != "" {
			req.Header.Set("X-Forwarded-Prefix", prefix)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Result().Body)
		return string(b)
	}
	if b := get("127.0.0.1:5000", "/uk"); !strings.Contains(b, `<base href="/uk/">`) {
		t.Fatalf("trusted prefix not injected: %s", b)
	}
	if b := get("203.0.113.9:5000", "/uk"); !strings.Contains(b, `<base href="/">`) || strings.Contains(b, "/uk/") {
		t.Fatalf("untrusted prefix honoured: %s", b)
	}
	if b := get("127.0.0.1:5000", ""); !strings.Contains(b, `<base href="/">`) {
		t.Fatalf("root base missing: %s", b)
	}
	for _, bad := range []string{"uk", "/uk/", "/uk//x", "/uk/../etc", "/uk x", "/"} {
		if b := get("127.0.0.1:5000", bad); !strings.Contains(b, `<base href="/">`) {
			t.Fatalf("malformed prefix %q not rejected: %s", bad, b)
		}
	}

	// The session cookie is scoped to the honoured prefix.
	req := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(`{"username":"root","password":"PrefixPass123!"}`))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Prefix", "/uk")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if sc := rec.Header().Get("Set-Cookie"); !strings.Contains(sc, "Path=/uk/") {
		t.Fatalf("cookie not scoped to prefix: %q", sc)
	}
}
