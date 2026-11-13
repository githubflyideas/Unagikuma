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

func TestHeartbeatShapedByTheUser(t *testing.T) {
	type call struct{ path, method, query, ctype, auth, body string }
	var mu sync.Mutex
	var got []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, call{r.URL.Path, r.Method, r.URL.RawQuery, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), string(b)})
		mu.Unlock()
	}))
	defer srv.Close()
	a, _, _, _ := newApp(t, testConfig)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	// The running app beats on its own too, so look for the call by path.
	last := func(path string) call {
		mu.Lock()
		defer mu.Unlock()
		for i := len(got) - 1; i >= 0; i-- {
			if got[i].path == path {
				return got[i]
			}
		}
		t.Fatalf("no call to %s", path)
		return call{}
	}

	// Plain URL: a GET, as before.
	if err := a.SetSetting("heartbeat_url", srv.URL+"/ping"); err != nil {
		t.Fatal(err)
	}
	if st := a.BeatNow(context.Background()); st.LastError != "" {
		t.Fatal(st.LastError)
	}
	if c := last("/ping"); c.method != "GET" || c.body != "" {
		t.Fatalf("plain beat: %+v", c)
	}

	// Variables in the URL are escaped for it (an Uptime Kuma push monitor).
	if err := a.SetSetting("heartbeat_url", srv.URL+"/api/push/x?status=up&msg={{.Instance}}-{{.Status}}"); err != nil {
		t.Fatal(err)
	}
	a.BeatNow(context.Background())
	if c := last("/api/push/x"); c.query != "status=up&msg=test-ok" {
		t.Fatalf("query = %q", c.query)
	}

	// POST with headers and a JSON body.
	for k, v := range map[string]string{"heartbeat_url": srv.URL + "/hb", "heartbeat_method": "post",
		"heartbeat_headers": "Authorization: Bearer abc\n", "heartbeat_body": `{"host":{{json .Instance}},"problems":{{.Problems}}}`} {
		if err := a.SetSetting(k, v); err != nil {
			t.Fatal(k, err)
		}
	}
	if st := a.BeatNow(context.Background()); st.LastError != "" {
		t.Fatal(st.LastError)
	}
	c := last("/hb")
	if c.method != "POST" || c.ctype != "application/json" || c.auth != "Bearer abc" || c.body != `{"host":"test","problems":0}` {
		t.Fatalf("shaped beat: %+v", c)
	}
}

func TestHeartbeatSettingsAreChecked(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	for k, v := range map[string]string{"heartbeat_method": "DELETE", "heartbeat_headers": "no colon here",
		"heartbeat_body": "{{.Instance", "heartbeat_url": "https://x/?a={{.Nope"} {
		if err := a.SetSetting(k, v); err == nil {
			t.Errorf("%s=%q accepted", k, v)
		}
	}
	if err := a.SetSetting("heartbeat_body", "{{.Nope}}"); err != nil {
		t.Fatal(err) // parses; the unknown name shows on the next beat
	}
	a.SetSetting("heartbeat_method", "POST")
	a.SetSetting("heartbeat_url", "http://127.0.0.1:1/")
	if st := a.BeatNow(context.Background()); !strings.Contains(st.LastError, "Nope") {
		t.Fatalf("unknown name not reported: %q", st.LastError)
	}
}
