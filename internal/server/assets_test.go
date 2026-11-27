package server

import (
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/web"
)

var (
	baseHref  = regexp.MustCompile(`<base href="([^"]*)">`)
	assetRefs = regexp.MustCompile(`<(?:link|script)[^>]+(?:href|src)="([^"]+)"`)
)

// TestPagesLoadTheirAssets loads every page of the real console the way a
// browser does — with and without a path-prefix proxy — and requires the
// <base> the server injects to be allowed by its own Content Security
// Policy and every stylesheet and script the page names to load from the
// address it resolves to. (In 3.1.1 base-uri 'none' made the browser
// ignore the <base>, so the public status page loaded no CSS or script.)
func TestPagesLoadTheirAssets(t *testing.T) {
	ui, err := web.FS()
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, testBind, nil)
	srv := New(e.app, Options{
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Web:          ui,
		TrustedProxy: func(ip net.IP) bool { return ip.IsLoopback() },
	})
	defer srv.Close()
	h := srv.Handler()
	get := func(path, prefix string) (int, string, string, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		if prefix != "" {
			req.Header.Set("X-Forwarded-Prefix", prefix)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Result().Body)
		return rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Security-Policy"), string(b)
	}
	for _, prefix := range []string{"", "/uk"} {
		for _, page := range []string{"/", "/status/main", "/wall", "/summary"} {
			code, _, csp, body := get(page, prefix)
			if code != 200 {
				t.Fatalf("%s%s: %d", prefix, page, code)
			}
			if !strings.Contains(csp, "base-uri 'self'") {
				t.Fatalf("%s%s: the CSP does not allow the page's <base>: %q", prefix, page, csp)
			}
			m := baseHref.FindStringSubmatch(body)
			if m == nil || m[1] != prefix+"/" {
				t.Fatalf("%s%s: <base> %v", prefix, page, m)
			}
			pageURL, _ := url.Parse("http://h" + prefix + page)
			base := pageURL.ResolveReference(&url.URL{Path: m[1]})
			refs := assetRefs.FindAllStringSubmatch(body, -1)
			if len(refs) == 0 {
				t.Fatalf("%s%s names no assets", prefix, page)
			}
			for _, ref := range refs {
				u, err := url.Parse(ref[1])
				if err != nil || u.Host != "" {
					continue
				}
				abs := base.ResolveReference(u)
				if !strings.HasPrefix(abs.Path, prefix+"/") {
					t.Fatalf("%s%s: %s resolves outside the prefix: %s", prefix, page, ref[1], abs.Path)
				}
				// The proxy strips the prefix before passing it on.
				code, ctype, _, _ := get(strings.TrimPrefix(abs.Path, prefix), prefix)
				want := map[bool]string{true: "text/css", false: "javascript"}[strings.HasSuffix(abs.Path, ".css")]
				if strings.HasSuffix(abs.Path, ".png") || strings.HasSuffix(abs.Path, ".svg") || strings.HasSuffix(abs.Path, ".ico") {
					want = "image/"
				}
				if strings.HasSuffix(abs.Path, ".json") || strings.HasSuffix(abs.Path, ".webmanifest") {
					want = ""
				}
				if code != 200 || !strings.Contains(ctype, want) {
					t.Errorf("%s%s: %s → %s: %d %s", prefix, page, ref[1], abs.Path, code, ctype)
				}
			}
		}
	}
}
