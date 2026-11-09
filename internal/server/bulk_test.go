package server

import (
	"strings"
	"testing"
)

func TestMonitorBulkDelete(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	cfg := func() string { return e.app.Config().ETag() }
	if c := e.do("POST", "/api/v1/monitors/bulk-delete", map[string]any{"names": []string{}}, nil); c != 400 {
		t.Fatalf("empty: %d", c)
	}
	if c := e.do("POST", "/api/v1/monitors/bulk-delete", map[string]any{"names": []string{"web", "nope"}}, nil); c != 404 || e.app.Config().Config.FindMonitor("web") == nil {
		t.Fatalf("unknown name: %d", c)
	}
	// A stale etag fails the whole request: nothing is deleted.
	before := cfg()
	if c := e.do("POST", "/api/v1/monitors/bulk-delete", map[string]any{"names": []string{"web", "db"}, "etags": map[string]string{"db": `"stale"`}}, nil); c != 412 {
		t.Fatalf("stale etag: %d", c)
	}
	if cfg() != before || e.app.Config().Config.FindMonitor("web") == nil {
		t.Fatal("a refused request changed the file")
	}
	serial := e.app.Config().Serial
	var st struct{ Serial uint32 }
	if c := e.do("POST", "/api/v1/monitors/bulk-delete", map[string]any{"names": []string{"web", "db"}}, &st); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	c := e.app.Config().Config
	if c.FindMonitor("web") != nil || c.FindMonitor("db") != nil || c.FindMonitor("backup") == nil {
		t.Fatal("wrong monitors deleted")
	}
	if e.app.Config().Serial != st.Serial || st.Serial <= serial {
		t.Fatalf("serial %d → %d (response %d)", serial, e.app.Config().Serial, st.Serial)
	}
}

func TestMonitorBulkPause(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	paused := func(name string) bool { return e.app.Config().Config.FindMonitor(name).Disabled }
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{}, "paused": true}, nil); c != 400 {
		t.Fatalf("empty: %d", c)
	}
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"web", "nope"}, "paused": true}, nil); c != 404 || paused("web") {
		t.Fatalf("unknown name: %d", c)
	}
	// A stale etag refuses the whole batch.
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"web", "db"}, "paused": true, "etags": map[string]string{"db": `"stale"`}}, nil); c != 412 || paused("web") || paused("db") {
		t.Fatalf("stale etag: %d", c)
	}
	var st struct {
		Changed int      `json:"changed"`
		Skipped []string `json:"skipped"`
	}
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"web", "db", "web"}, "paused": true}, &st); c != 200 || st.Changed != 2 {
		t.Fatalf("pause: %d %+v", c, st)
	}
	if !paused("web") || !paused("db") || paused("backup") {
		t.Fatal("wrong monitors paused")
	}
	// Pausing again changes nothing; one already paused and one running
	// only touches the running one.
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"web", "backup"}, "paused": true}, &st); c != 200 || st.Changed != 1 || !paused("backup") {
		t.Fatalf("pause again: %d %+v", c, st)
	}
	serial := e.app.Config().Serial
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"web"}, "paused": true}, &st); c != 200 || st.Changed != 0 || e.app.Config().Serial != serial {
		t.Fatalf("no-op wrote the file: %d %+v", c, st)
	}
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"web", "db", "backup"}, "paused": false}, &st); c != 200 || st.Changed != 3 || len(st.Skipped) != 0 {
		t.Fatalf("resume: %d %+v", c, st)
	}
	if paused("web") || paused("db") || paused("backup") {
		t.Fatal("still paused after resume")
	}
	if strings.Contains(e.app.Config().Text, "disabled") {
		t.Fatal("resume left disabled= in the file")
	}
}
