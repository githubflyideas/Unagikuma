package store

import (
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"strings"
	"testing"
	"time"
)

func TestLegacyClusterMigrationNeverInventsRecovery(t *testing.T) {
	s, c, _ := open(t)
	auditN(t, s, c, 1)
	st := engine.IncidentState{OpenedAt: time.Now(), Acked: true, AckedBy: "old-operator"}
	for _, kind := range []string{engine.IncidentNormal, engine.IncidentRegional} {
		if _, err := s.core.Exec(`INSERT INTO incidents(monitor,kind,state,opened_at,updated_at) VALUES('web',?,?,1,1)`, kind, mustJSON(st)); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []*engine.Intent{{ID: "old-problem", IncidentID: 1, Kind: "PROBLEM", State: engine.IntentPending, Message: notify.Message{IncidentID: 1}}, {ID: "old-regional", IncidentID: 2, Kind: "PROBLEM", State: engine.IntentPending, Message: notify.Message{IncidentID: 2}}} {
		if err := s.SaveIntent(in); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MigrateIndependentSite("a", true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		inc, err := s.Incident(id)
		if err != nil || !inc.State.Closed || !inc.State.ResolvedAt.IsZero() || !inc.State.Acked || !inc.HistoryOnly || !strings.HasPrefix(inc.GlobalID, "a:") {
			t.Fatalf("legacy record %+v %v", inc, err)
		}
	}
	if pending, _ := s.PendingIntents(); len(pending) != 0 {
		t.Fatal("legacy notices still queued", pending)
	}
	if v, err := s.VerifyAudit(); err != nil || !v.OK || v.Entries != 2 {
		t.Fatalf("audit %+v %v", v, err)
	}
	if err := s.MigrateIndependentSite("a", true); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.VerifyAudit(); v.Entries != 2 {
		t.Fatal("migration not idempotent", v)
	}
}
func TestSingleSiteMigrationPreservesAckAndQueue(t *testing.T) {
	s, _, _ := open(t)
	st := engine.IncidentState{OpenedAt: time.Now(), Acked: true, AckedBy: "operator"}
	if _, err := s.core.Exec(`INSERT INTO incidents(monitor,kind,state,opened_at,updated_at) VALUES('web','normal',?,1,1)`, mustJSON(st)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIntent(&engine.Intent{ID: "queued", IncidentID: 1, State: engine.IntentPending, Kind: "PROBLEM", Message: notify.Message{IncidentID: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateIndependentSite("solo", false); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Incident(1)
	queue, _ := s.PendingIntents()
	if before.State.Closed || !before.State.Acked || len(queue) != 1 || queue[0].Message.GlobalIncidentID != before.GlobalID {
		t.Fatalf("lost single-site state %+v %+v", before, queue)
	}
	oldEpoch, _ := s.DBEpoch()
	if err := s.RotateDBEpoch("solo"); err != nil {
		t.Fatal(err)
	}
	nextEpoch, _ := s.DBEpoch()
	after, _ := s.Incident(1)
	if oldEpoch == nextEpoch || before.GlobalID != after.GlobalID {
		t.Fatal("restore reused epoch or changed history", oldEpoch, nextEpoch, before, after)
	}
	next := &engine.Incident{Kind: engine.IncidentNormal, Monitor: "next", SourceNode: "solo"}
	if err := s.SaveIncident(next); err != nil {
		t.Fatal(err)
	}
	if next.SourceEpoch != nextEpoch {
		t.Fatal("new incident reused old namespace", next)
	}
}

func TestHistorySourceIDsSurviveFullRetentionPrune(t *testing.T) {
	s, _, _ := open(t)
	if _, err := s.hist.Exec("INSERT INTO rounds(rowid,mid,at,st,flags) VALUES(50,1,1,1,0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.hist.Exec("DELETE FROM rounds"); err != nil {
		t.Fatal(err)
	}
	res, err := s.hist.Exec("INSERT INTO rounds(mid,at,st,flags) VALUES(1,2,1,0)")
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil || id <= 50 {
		t.Fatal("pruning recycled an exported source ID/cursor", id, err)
	}
}
