package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newHeartbeat(url string) *Heartbeat {
	return &Heartbeat{
		Client:    &http.Client{},
		URL:       func() string { return url },
		Interval:  func() time.Duration { return 10 * time.Millisecond },
		Timeout:   func() time.Duration { return time.Second },
		Now:       func() time.Time { return t0 },
		UserAgent: "unagikuma/test",
	}
}

func TestHeartbeatOK(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "unagikuma/test" {
			t.Errorf("ua %q", r.Header.Get("User-Agent"))
		}
		hits.Add(1)
	}))
	defer srv.Close()
	h := newHeartbeat(srv.URL + "/ping/secret-uuid")
	h.Beat(context.Background())
	st := h.Status()
	if !st.Configured || !st.LastOK.Equal(t0) || st.LastError != "" || hits.Load() != 1 {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(st.Target, "secret") {
		t.Fatalf("target leaks the secret: %q", st.Target)
	}
}

func TestHeartbeatFailureRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	h := newHeartbeat(srv.URL + "/ping/secret-uuid?token=abc")
	h.Beat(context.Background())
	st := h.Status()
	if st.LastError == "" || !st.LastOK.IsZero() || strings.Contains(st.LastError, "secret") || strings.Contains(st.LastError, "abc") {
		t.Fatalf("%+v", st)
	}
	// Connection refused: the error must not echo the URL either.
	srv.Close()
	h.Beat(context.Background())
	if st := h.Status(); st.LastError == "" || strings.Contains(st.LastError, "secret") {
		t.Fatalf("%+v", st)
	}
}

func TestHeartbeatDisabledAndRun(t *testing.T) {
	h := newHeartbeat("")
	h.Beat(context.Background())
	if st := h.Status(); st.Configured || !st.LastTry.IsZero() {
		t.Fatalf("%+v", st)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	h = newHeartbeat(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	h.Run(ctx)
	if hits.Load() < 2 {
		t.Fatalf("beats %d", hits.Load())
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"":                         "",
		"https://hc-ping.com/uuid": "https://hc-ping.com/…",
		"https://u:p@example.com/": "https://example.com",
		"http://10.0.0.2:8077?x=1": "http://10.0.0.2:8077/…",
		"not a url":                "(invalid URL)",
		"https://example.com":      "https://example.com",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q)=%q want %q", in, got, want)
		}
	}
}
