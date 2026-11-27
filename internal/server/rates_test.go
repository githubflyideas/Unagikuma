package server

import (
	"strings"
	"testing"
)

// A rate tunable changed in the configuration holds at once: the tunables
// page shows the new value, and the limiter must follow it, not keep the
// rate it had at start until a restart.
func TestRateTunableAppliesAtOnce(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	bearer := &env{t: t, app: e.app, srv: e.srv, ts: e.ts, c: e.ts.Client()}
	// limited: a new token (a full bucket) is refused within 15 requests.
	limited := func() bool {
		var tok struct{ Token string }
		if e.do("POST", "/api/v1/tokens", map[string]any{"name": "rate", "scopes": []string{"read"}}, &tok) != 200 {
			return false
		}
		for range 15 {
			if c := bearer.do("GET", "/api/v1/overview", nil, nil, "Authorization", "Bearer "+tok.Token); c == 429 {
				return true
			}
		}
		return false
	}
	if limited() {
		t.Fatal("limited at the default rate (20/s, burst 200)")
	}
	var cfg struct{ Text, ETag string }
	e.do("GET", "/api/v1/config", nil, &cfg)
	text := strings.Replace(cfg.Text, "\n", "\n$SET api.rate=1/s\n", 1)
	if c := e.do("PUT", "/api/v1/config", map[string]string{"text": text}, nil, "If-Match", cfg.ETag); c != 200 {
		t.Fatalf("save: %d", c)
	}
	waitUntil(t, limited)
}
