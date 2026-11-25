package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/ratelimit"
)

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, rateLimited(1500*time.Millisecond))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "2" || !strings.Contains(rec.Body.String(), `"retry_after_s":2`) {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	rec = httptest.NewRecorder()
	writeError(rec, errors.New("db exploded: secret path /var/lib"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("internal errors must not leak: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	writeError(rec, &APIError{Status: 400, Code: CodeInvalid, Message: "bad", Location: &Location{File: "monitors.conf", Line: 3, Col: 7}})
	if !strings.Contains(rec.Body.String(), `"line":3`) {
		t.Fatalf("%s", rec.Body)
	}
}

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	req := func(s string) *http.Request { return httptest.NewRequest("POST", "/", strings.NewReader(s)) }
	var b body
	if err := decodeJSON(req(`{"name":"x"}`), 100, &b); err != nil || b.Name != "x" {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"nmae":"x"}`, `{"name":"x"} {}`, `[`, ``} {
		if err := decodeJSON(req(bad), 100, &b); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	err := decodeJSON(req(`{"name":"`+strings.Repeat("a", 200)+`"}`), 100, &b)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 413 {
		t.Fatalf("too large: %v", err)
	}
}

func TestSecurityHeadersAndRecover(t *testing.T) {
	h := securityHeaders(recoverer(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(r, nil); got != "10.0.0.5" {
		t.Fatalf("untrusted proxy header used: %s", got)
	}
	trusted := func(ip net.IP) bool { return ip.IsPrivate() }
	if got := clientIP(r, trusted); got != "5.6.7.8" {
		t.Fatalf("trusted proxy: %s", got)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := &limiter{keyed: ratelimit.NewKeyed(1, 2, 0), now: func() time.Time { return now }, key: func(r *http.Request) string { return "k" }}
	h := l.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	codes := []int{}
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		codes = append(codes, rec.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("%v", codes)
	}
}

// readEvents reads SSE frames until n non-comment frames arrived.
func readEvents(t *testing.T, br *bufio.Reader, n int) []map[string]string {
	t.Helper()
	var out []map[string]string
	cur := map[string]string{}
	for len(out) < n {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v (got %v)", err, out)
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			if len(cur) > 0 && cur["retry"] == "" {
				out = append(out, cur)
			}
			cur = map[string]string{}
			continue
		}
		k, v, _ := strings.Cut(line, ": ")
		cur[k] = v
	}
	return out
}

func TestHubLiveAndReplay(t *testing.T) {
	hub := NewHub("b1", 4, func() int { return 10 })
	srv := httptest.NewServer(hub)
	defer srv.Close()

	hub.Publish("event", map[string]string{"m": "one"})
	hub.Publish("event", map[string]string{"m": "two"})

	// Reconnect after event 1: event 2 is replayed.
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Last-Event-ID", "b1-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("ct %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	evs := readEvents(t, br, 1)
	if evs[0]["id"] != "b1-2" || !strings.Contains(evs[0]["data"], "two") {
		t.Fatalf("replay: %v", evs)
	}
	// Live delivery.
	for hub.Clients() == 0 {
		time.Sleep(time.Millisecond)
	}
	hub.Publish("state", map[string]string{"name": "web"})
	evs = readEvents(t, br, 1)
	if evs[0]["event"] != "state" || evs[0]["id"] != "b1-3" {
		t.Fatalf("live: %v", evs)
	}
}

func TestHubReset(t *testing.T) {
	hub := NewHub("b2", 2, nil)
	for i := 0; i < 5; i++ {
		hub.Publish("event", i)
	}
	cases := map[string]bool{
		"":       false, // fresh client: no replay, no reset
		"b1-3":   true,  // other boot
		"b2-1":   true,  // overwritten
		"b2-3":   false, // 4 and 5 still buffered
		"b2-5":   false,
		"b2-9":   true, // from the future
		"garbag": true,
	}
	for id, wantReset := range cases {
		evs, reset := hub.replayFrom(id)
		if reset != wantReset {
			t.Errorf("%q: reset=%v", id, reset)
		}
		if id == "b2-3" && (len(evs) != 2 || evs[0].seq != 4) {
			t.Errorf("replay %v", evs)
		}
	}
}

func TestHubLimitsAndSlowClients(t *testing.T) {
	hub := NewHub("b3", 8, func() int { return 1 })
	s1, _, _, ok := hub.subscribe("")
	if !ok {
		t.Fatal("first")
	}
	if _, _, _, ok := hub.subscribe(""); ok {
		t.Fatal("over the limit")
	}
	for i := 0; i < subBuffer+1; i++ {
		hub.Publish("event", i)
	}
	if hub.Clients() != 0 || !s1.dropped {
		t.Fatal("slow client must be dropped")
	}
	hub.unsubscribe(s1) // idempotent after a drop
}
