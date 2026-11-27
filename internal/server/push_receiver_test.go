package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A push token whose monitor is no longer a push monitor answers like an
// unknown token (404, the Uptime Kuma shape), not with an internal error.
func TestPushToANonPushMonitor(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	var p struct{ Token, Path string }
	if code := e.do("GET", "/api/v1/monitors/backup/push", nil, &p); code != 200 || p.Token == "" {
		t.Fatalf("push token: %d", code)
	}
	var cfg struct{ Text, ETag string }
	e.do("GET", "/api/v1/config", nil, &cfg)
	text := strings.Replace(cfg.Text, "backup  IN PUSH period=1h", "backup  IN TCP 192.168.1.30:22", 1)
	if text == cfg.Text {
		t.Fatal("the test configuration has no push monitor line to change")
	}
	if code := e.do("PUT", "/api/v1/config", map[string]string{"text": text}, nil, "If-Match", cfg.ETag); code != 200 {
		t.Fatalf("save: %d", code)
	}
	anon := &env{t: t, ts: e.ts, c: &http.Client{Timeout: 10 * time.Second}}
	var pr struct {
		OK  bool   `json:"ok"`
		Msg string `json:"msg"`
	}
	if code := anon.do("GET", p.Path+"?status=up", nil, &pr); code != 404 || pr.OK || pr.Msg == "" {
		t.Fatalf("push to a TCP monitor: %d %+v", code, pr)
	}
}
