package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestBuildHeartbeat(t *testing.T) {
	in := HeartbeatInfo{Host: "nas01", Status: "ok", Problems: 0, Monitors: 47, Time: "2026-11-13T08:00:00Z"}
	cases := []struct {
		name string
		set  map[string]string
		url  string
		want func(t *testing.T, method, url, auth, key, ctype, body string)
	}{
		{"plain GET with host info", nil, "https://hc-ping.com/abc", func(t *testing.T, m, u, _, _, _, b string) {
			if m != "GET" || b != "" || u != "https://hc-ping.com/abc?host=nas01&monitors=47&problems=0&status=ok&time=2026-11-13T08%3A00%3A00Z" {
				t.Fatalf("%s %s %q", m, u, b)
			}
		}},
		{"GET without host info keeps the URL as written", map[string]string{"heartbeat_host_info": "off"}, "https://k.example/api/push/x?status=up&msg=OK&ping=", func(t *testing.T, m, u, _, _, _, _ string) {
			if u != "https://k.example/api/push/x?status=up&msg=OK&ping=" {
				t.Fatal(u)
			}
		}},
		{"the receiver's own parameters win", nil, "https://k.example/api/push/x?status=up", func(t *testing.T, _, u, _, _, _, _ string) {
			if !strings.Contains(u, "status=up") || strings.Contains(u, "status=ok") {
				t.Fatal(u)
			}
		}},
		{"PUSH JSON with basic auth", map[string]string{"heartbeat_method": "PUSH", "heartbeat_auth": "basic", "heartbeat_user": "u", "heartbeat_password": "p"}, "https://x.example/hb", func(t *testing.T, m, u, auth, _, ct, b string) {
			if m != "POST" || u != "https://x.example/hb" || auth != "Basic dTpw" || ct != "application/json" ||
				b != `{"host":"nas01","status":"ok","problems":0,"monitors":47,"time":"2026-11-13T08:00:00Z"}` {
				t.Fatalf("%s %s %s %s %s", m, u, auth, ct, b)
			}
		}},
		{"PUSH text with an API key header", map[string]string{"heartbeat_method": "PUSH", "heartbeat_format": "text", "heartbeat_auth": "apikey", "heartbeat_key_value": "k1"}, "https://x.example/hb", func(t *testing.T, _, _, _, key, ct, b string) {
			if key != "k1" || !strings.HasPrefix(ct, "text/plain") || b != "nas01 ok 0 problems 47 monitors 2026-11-13T08:00:00Z" {
				t.Fatalf("%s %s %q", key, ct, b)
			}
		}},
		{"API key as a URL parameter", map[string]string{"heartbeat_host_info": "off", "heartbeat_auth": "apikey", "heartbeat_key_name": "token", "heartbeat_key_value": "k1", "heartbeat_key_in": "query"}, "https://x.example/hb", func(t *testing.T, _, u, _, _, _, _ string) {
			if u != "https://x.example/hb?token=k1" {
				t.Fatal(u)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hr, err := buildHeartbeat(func(k string) string { return c.set[k] }, c.url, in)
			if err != nil {
				t.Fatal(err)
			}
			c.want(t, hr.Method, hr.URL, hr.Header.Get("Authorization"), hr.Header.Get("X-Api-Key"), hr.Header.Get("Content-Type"), hr.Body)
		})
	}
	if _, err := buildHeartbeat(func(string) string { return "" }, "ftp://x/", in); err == nil {
		t.Fatal("ftp accepted")
	}
}

func TestHeartbeatSentAndPreviewed(t *testing.T) {
	var mu sync.Mutex
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		if r.Method == "POST" {
			gotAuth, gotBody = r.Header.Get("Authorization"), string(b)
		}
		mu.Unlock()
	}))
	defer srv.Close()
	a, _, _, _ := newApp(t, testConfig)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	for k, v := range map[string]string{"heartbeat_url": srv.URL + "/hb", "heartbeat_method": "PUSH", "heartbeat_auth": "basic",
		"heartbeat_user": "u", "heartbeat_password": "secret"} {
		if err := a.SetSetting(k, v); err != nil {
			t.Fatal(k, err)
		}
	}
	if st := a.BeatNow(context.Background()); st.LastError != "" {
		t.Fatal(st.LastError)
	}
	mu.Lock()
	if gotAuth != "Basic dTpzZWNyZXQ=" || !strings.Contains(gotBody, `"host":"test"`) {
		t.Fatalf("auth %q body %q", gotAuth, gotBody)
	}
	mu.Unlock()
	// The form sends the saved password back as ***: the preview uses the
	// saved one and shows it masked.
	pv, err := a.PreviewHeartbeat(map[string]string{"heartbeat_url": srv.URL + "/hb", "heartbeat_method": "PUSH", "heartbeat_auth": "basic",
		"heartbeat_user": "u", "heartbeat_password": "***"})
	if err != nil || pv.Header.Get("Authorization") != "Basic ***" {
		t.Fatalf("preview %v %v", pv.Header, err)
	}
	if err := a.SetSetting("heartbeat_method", "DELETE"); err == nil {
		t.Fatal("DELETE accepted")
	}
}
