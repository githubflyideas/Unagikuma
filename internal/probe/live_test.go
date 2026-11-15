//go:build live

package probe

import (
	"strings"
	"testing"
)

// Checks every application profile against the real application, started
// by scripts/live/start.sh. Not part of the normal tests: they need the
// applications installed. A profile's "verified" says which version it
// was last run against.
func TestLiveApps(t *testing.T) {
	for _, c := range []struct {
		app, url string
		field    string
		min, max float64 // the judged number must lie in [min, max]
	}{
		{"nginx", "http://127.0.0.1:18080/nginx_status", "active", 1, 100},
		{"nginx", "http://127.0.0.1:18080/nginx_status", "requests", 1, 1e9},
		{"apache", "http://127.0.0.1:18081/server-status?auto", "busy", 1, 100},
		{"lighttpd", "http://127.0.0.1:18082/server-status?auto", "busy", 0, 100},
		{"php-fpm", "http://127.0.0.1:18080/status", "total", 1, 10},
		{"haproxy", "http://127.0.0.1:18084/metrics", "sessions", 0, 100},
		{"haproxy", "http://127.0.0.1:18084/metrics", "servers_down", 1, 1}, // s2 is a closed port
		{"haproxy", "http://127.0.0.1:18084/metrics", "backends_up", 1, 1},
	} {
		r := run(t, Spec{Type: "HTTP", Target: c.url, Opts: map[string]string{"app": c.app, "field": c.field}})
		if r.Vote != VoteOK {
			t.Errorf("%s %s: %s %s", c.app, c.field, r.Reason, r.Detail)
			continue
		}
		if v := r.Metrics["value"]; v < c.min || v > c.max {
			t.Errorf("%s %s = %v, want %v..%v (%s)", c.app, c.field, v, c.min, c.max, r.Detail)
		}
		if !strings.Contains(r.Detail, ":") {
			t.Errorf("%s: detail %q", c.app, r.Detail)
		}
	}
}
