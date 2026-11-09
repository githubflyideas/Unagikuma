package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/oncall"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Response times count against whoever took the problem notice; for
// incidents from before that was kept, whoever was on call when it opened:
// the scheduled member, or the stand-in covering them.
func TestOnCallStats(t *testing.T) {
	dir := t.TempDir()
	bind := `@ IN SOA unagikuma. admin.localhost. ( 1 1m 15s 5s 3 )
li      IN CHANNEL log
wang    IN CHANNEL log
oncall  IN CHANNEL group members=li,wang rotate=daily handoff=00:00 start=2026-11-01
default IN POLICY chain=oncall
quiet   IN POLICY chain=li
web     IN TCP 192.168.1.10:80
`
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(bind), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run, Now: func() time.Time { return now }, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	st := a.Store()
	// Nov 1 and 3 are li's days, Nov 2 and 4 wang's.
	responders := ""
	inc := func(policy string, opened time.Time, ack, res time.Duration) {
		t.Helper()
		in := &engine.Incident{Monitor: "web", Kind: engine.IncidentNormal, Policy: policy, Title: "web"}
		in.State.OpenedAt, in.State.Responders = opened, responders
		if res > 0 {
			in.State.ResolvedAt = opened.Add(res)
		}
		if err := st.SaveIncident(in); err != nil {
			t.Fatal(err)
		}
		if ack > 0 {
			if err := st.AddAck(in.ID, store.AckRecord{At: opened.Add(ack), By: "x", Via: "web", Action: "ack"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	day := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	inc("default", day(1, 3), 2*time.Minute, 30*time.Minute)    // li
	inc("default", day(3, 3), 4*time.Minute, 0)                 // li, still open
	inc("default", day(2, 3), 10*time.Minute, time.Hour)        // wang
	inc("default", day(4, 3), 0, 20*time.Minute)                // wang's day, but covered by li
	inc("quiet", day(2, 5), time.Minute, time.Minute)           // not paged through the rotation
	inc("default", day(1, 3).AddDate(0, -2, 0), time.Minute, 0) // outside the window
	// li's day, but li could not be reached and wang took it.
	responders = "oncall=wang"
	inc("default", day(3, 5), 0, 0)
	responders = ""
	if _, err := st.AddOverride(oncall.Override{Rotation: "oncall", Member: "li", From: day(4, 0), Until: day(4, 6), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	res, err := a.OnCallStats(30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.All.Incidents != 6 || res.All.Acked != 4 || res.All.Resolved != 4 {
		t.Fatalf("all: %+v", res.All)
	}
	if len(res.Members) != 2 || res.Members[0].Member != "li" || res.Members[1].Member != "wang" {
		t.Fatalf("members: %+v", res.Members)
	}
	li, wang := res.Members[0], res.Members[1]
	if li.Incidents != 3 || li.Acked != 2 || li.MTTA != 180 || li.Resolved != 2 || li.MTTR != 25*60 {
		t.Fatalf("li: %+v", li)
	}
	if wang.Incidents != 2 || wang.Acked != 1 || wang.MTTA != 600 || wang.MTTR != 3600 {
		t.Fatalf("wang: %+v", wang)
	}
}
