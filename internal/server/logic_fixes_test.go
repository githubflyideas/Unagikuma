package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A status page earns its overall "ok": a page with no monitors, or
// only monitors that were never judged, reports "unknown" — not a
// default "ok" with no evidence behind it.
func TestStatusPageOverallMustBeEarned(t *testing.T) {
	bind := testBind + `empty IN STATUSPAGE "Empty page" select="group:nothing"
pushy IN STATUSPAGE "Push page" select="backup"
`
	e := newEnv(t, bind, nil)
	e.setup()
	anon := &env{t: t, ts: e.ts, c: &http.Client{Timeout: 10 * time.Second}}
	overall := func(page string) string {
		t.Helper()
		var sp struct {
			Status   string
			Monitors []map[string]any
		}
		if code := anon.do("GET", "/api/v1/status/"+page, nil, &sp); code != 200 {
			t.Fatalf("status page %s: %d", page, code)
		}
		return sp.Status
	}
	if got := overall("empty"); got != "unknown" {
		t.Fatalf("empty page overall = %q, want unknown", got)
	}
	// backup is a PUSH monitor that has never pushed: never judged.
	if got := overall("pushy"); got != "unknown" {
		t.Fatalf("never-judged page overall = %q, want unknown", got)
	}
	// Once web is really ok, the main page (group:public) earns its ok.
	waitUntil(t, func() bool {
		r, ok := e.app.Engine().Row("web")
		return ok && r.Status == "ok"
	})
	if got := overall("main"); got != "ok" {
		t.Fatalf("healthy page overall = %q, want ok", got)
	}
}

// A monitor that has never been measured reports latency_ms as null
// (like uptime_24h), and its latency badge says n/a — never a
// measured-looking "0 ms". After a real push, both show the value.
func TestLatencyNullUntilMeasured(t *testing.T) {
	// Badges exist only for published monitors: put backup on a page.
	e := newEnv(t, testBind+"pushy IN STATUSPAGE \"Push page\" select=\"backup\"\n", nil)
	e.setup()
	raw := func(path string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v: %s", path, err, b)
		}
		return m
	}
	latencyOf := func(path string) (any, bool) {
		row, _ := raw(path)["row"].(map[string]any)
		v, ok := row["latency_ms"]
		return v, ok
	}
	if v, ok := latencyOf("/api/v1/monitors/backup"); !ok || v != nil {
		t.Fatalf("unmeasured latency_ms = %v (present %v), want JSON null", v, ok)
	}
	badge := func() string {
		t.Helper()
		resp, err := http.Get(e.ts.URL + "/badge/backup/latency.svg")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if b := badge(); !strings.Contains(b, "n/a") || strings.Contains(b, "0 ms") {
		t.Fatalf("unmeasured badge: %s", b)
	}

	// Push a real measurement: latency appears, in the API and the badge.
	var p struct{ Token, Path string }
	if code := e.do("GET", "/api/v1/monitors/backup/push", nil, &p); code != 200 || p.Token == "" {
		t.Fatalf("push token: %d", code)
	}
	anon := &env{t: t, ts: e.ts, c: &http.Client{Timeout: 10 * time.Second}}
	var pr struct{ OK bool }
	if code := anon.do("GET", p.Path+"?status=up&ping=12", nil, &pr); code != 200 || !pr.OK {
		t.Fatalf("push: %d", code)
	}
	waitUntil(t, func() bool {
		v, _ := latencyOf("/api/v1/monitors/backup")
		return v != nil
	})
	if v, _ := latencyOf("/api/v1/monitors/backup"); v != float64(12) {
		t.Fatalf("measured latency_ms = %v, want 12", v)
	}
	if b := badge(); !strings.Contains(b, "12 ms") {
		t.Fatalf("measured badge: %s", b)
	}
}
