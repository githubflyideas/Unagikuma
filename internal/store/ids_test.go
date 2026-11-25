package store

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
)

func TestRenameKeepsHistoryAndRetiredComeBack(t *testing.T) {
	s, c, _ := open(t)
	if err := s.SyncMonitors([]MonitorDef{{"web", "h1"}, {"db", "h2"}}); err != nil {
		t.Fatal(err)
	}
	id := s.MonitorID("web")
	at := t0
	for i := 0; i < 30; i++ {
		s.Verdict(verdict("web", at, "ok", true, 10))
		at = at.Add(time.Minute)
	}
	s.Flush()
	inc := &engine.Incident{Monitor: "web", Kind: engine.IncidentNormal}
	if err := s.SaveIncident(inc); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameMonitor("web", "site"); err != nil {
		t.Fatal(err)
	}
	// A round queued under the old name lands on the renamed monitor.
	s.Verdict(verdict("web", at, "ok", true, 10))
	s.Flush()
	if err := s.SyncMonitors([]MonitorDef{{"site", "h1"}, {"db", "h2"}}); err != nil {
		t.Fatal(err)
	}
	c.t = at.Add(time.Minute)
	if s.MonitorID("site") != id || s.MonitorID("web") != id {
		t.Fatalf("ids: site=%d web=%d want %d", s.MonitorID("site"), s.MonitorID("web"), id)
	}
	u, _ := s.UptimeOf("site", t0, c.t)
	if u.Ratio == nil || *u.Ratio != 1 || u.UpS < 29*60 {
		t.Fatalf("history lost on rename: %+v", u)
	}
	if got, _ := s.Incident(inc.ID); got.Monitor != "site" {
		t.Fatalf("incident not moved: %+v", got)
	}

	// Deleted, then back with the same definition (a rollback): same id.
	if err := s.SyncMonitors([]MonitorDef{{"db", "h2"}}); err != nil {
		t.Fatal(err)
	}
	if s.MonitorID("site") != 0 {
		t.Fatal("deleted monitor still active")
	}
	if err := s.SyncMonitors([]MonitorDef{{"site", "h1"}, {"db", "h2"}}); err != nil {
		t.Fatal(err)
	}
	if s.MonitorID("site") != id {
		t.Fatal("restored monitor did not get its history back")
	}
	// A new monitor reusing the name for another check starts afresh.
	s.SyncMonitors([]MonitorDef{{"db", "h2"}})
	s.SyncMonitors([]MonitorDef{{"site", "other"}, {"db", "h2"}})
	if s.MonitorID("site") == id {
		t.Fatal("a different check under an old name must not inherit its history")
	}
	if u, _ := s.UptimeOf("site", t0, c.t); u.Ratio != nil {
		t.Fatalf("new monitor has old history: %+v", u)
	}
}

func TestFailingFromTheStartIsZero(t *testing.T) {
	s, c, _ := open(t)
	at := t0
	// attempts=3: two SOFT rounds, then HARD.
	for i := 0; i < 60; i++ {
		s.Verdict(verdict("new", at, "critical", i >= 2, 0))
		at = at.Add(time.Minute)
	}
	s.Flush()
	c.t = at
	u, _ := s.UptimeOf("new", t0, at)
	if u.Ratio == nil || *u.Ratio != 0 {
		t.Fatalf("a monitor that never worked must be 0%%: %+v", u)
	}
	day, _ := s.UptimeOf("new", at.Add(-24*time.Hour), at)
	if day.Coverage > 0.05 {
		t.Fatalf("one hour is not a day of data: coverage %.2f", day.Coverage)
	}
	// A SOFT failure that recovers counts as up.
	at = at.Add(time.Minute)
	for i := 0; i < 10; i++ {
		st := "ok"
		if i == 3 || i == 4 {
			st = "critical"
		}
		s.Verdict(verdict("flap", at, st, st == "ok", 0))
		at = at.Add(time.Minute)
	}
	s.Flush()
	c.t = at
	if u, _ := s.UptimeOf("flap", t0, at); u.Ratio == nil || *u.Ratio != 1 {
		t.Fatalf("recovered SOFT failure counted down: %+v", u)
	}
}

func TestDegradedTimeIsCounted(t *testing.T) {
	s, c, _ := open(t)
	at := t0
	for i := 0; i < 20; i++ {
		st := "ok"
		if i >= 10 {
			st = "degraded"
		}
		s.Verdict(verdict("slow", at, st, true, 900))
		at = at.Add(time.Minute)
	}
	s.Flush()
	c.t = at
	u, _ := s.UptimeOf("slow", t0, at)
	if u.Ratio == nil || *u.Ratio != 1 || u.DegradedS < 9*60 || u.DegradedS > 10*60 {
		t.Fatalf("%+v", u)
	}
}

func TestHistoryV1Migration(t *testing.T) {
	s, c, _ := open(t)
	// The first version's tables, as an older release left them.
	for _, q := range []string{
		`CREATE TABLE verdicts (monitor TEXT NOT NULL, at INTEGER NOT NULL, verdict TEXT NOT NULL, status TEXT NOT NULL,
			hard INTEGER NOT NULL, reason TEXT, latency_ms REAL, excluded INTEGER NOT NULL DEFAULT 0, fail_nodes TEXT)`,
		`CREATE TABLE agg1h (monitor TEXT NOT NULL, hour INTEGER NOT NULL, up_s REAL NOT NULL, down_s REAL NOT NULL,
			excluded_s REAL NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, lat_max REAL NOT NULL, PRIMARY KEY (monitor, hour))`,
	} {
		if _, err := s.hist.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old := t0.Add(-30 * 24 * time.Hour).Truncate(time.Hour)
	if _, err := s.hist.Exec(`INSERT INTO agg1h VALUES ('legacy', ?, 3000, 600, 0, 200, 10, 30)`, old.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.hist.Exec(`INSERT INTO verdicts VALUES ('legacy', ?, 'critical', 'critical', 1, 'conn_refused', NULL, 0, '["bj"]')`, ms(t0.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateHistoryV2(); err != nil {
		t.Fatal(err)
	}
	c.t = t0
	u, _ := s.UptimeOf("legacy", old, old.Add(time.Hour))
	if u.Ratio == nil || u.UpS != 3000 || u.DownS != 600 {
		t.Fatalf("hourly not carried over: %+v", u)
	}
	cells, _ := s.Cells("legacy", t0.Add(-2*time.Hour), t0, 2)
	if cells[1].Status != "critical" {
		t.Fatalf("rounds not carried over: %+v", cells)
	}
	var n int
	s.hist.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('verdicts','agg1h')`).Scan(&n)
	if n != 0 {
		t.Fatal("old tables left behind")
	}
}
