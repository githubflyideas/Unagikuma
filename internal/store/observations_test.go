package store

import (
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/observation"
	"testing"
	"time"
)

func TestObservationDeltaAndStaleness(t *testing.T) {
	src, c, _ := open(t)
	dst, dc, _ := open(t)
	put := func(mon, status string) {
		t.Helper()
		if err := src.RecordSiteObservation(observation.Row{NodeID: "a", MonitorID: mon, Status: status, CheckedAt: c.t, IntervalMS: 1000, DefHash: "v1"}); err != nil {
			t.Fatal(err)
		}
	}
	put("web", "up")
	put("db", "up")
	d, err := src.SiteObservationDelta("a", "", 0)
	if err != nil || len(d.Rows) != 2 {
		t.Fatal(d, err)
	}
	if err := dst.ApplySiteObservationDelta("a", d); err != nil {
		t.Fatal(err)
	}
	snap, err := dst.SiteObservationSnapshot("web", "v1", []string{"a", "b"}, dc.t)
	if err != nil || snap.Rows[0].Status != "up" || snap.Rows[0].Stale || !snap.Rows[1].Stale {
		t.Fatal(snap, err)
	}
	c.t = c.t.Add(time.Second)
	dc.t = dc.t.Add(time.Second)
	put("web", "down")
	next, err := src.SiteObservationDelta("a", d.Epoch, d.Cursor)
	if err != nil || len(next.Rows) != 1 || next.Rows[0].MonitorID != "web" {
		t.Fatal(next, err)
	}
	if err := dst.ApplySiteObservationDelta("a", next); err != nil {
		t.Fatal(err)
	}
	if snap.Rows[0].Status != "up" {
		t.Fatal("snapshot mutated")
	}
	same, err := src.SiteObservationDelta("a", next.Epoch, next.Cursor)
	if err != nil || len(same.Rows) != 0 {
		t.Fatal(same, err)
	}
	if err := dst.ApplySiteObservationDelta("a", d); err == nil {
		t.Fatal("cursor regression accepted")
	}
	dc.t = dc.t.Add(4 * time.Second)
	stale, _ := dst.SiteObservationSnapshot("web", "v1", []string{"a"}, dc.t)
	if !stale.Rows[0].Stale {
		t.Fatal("not stale", stale)
	}
	d.NodeID = "forged"
	if err := dst.ApplySiteObservationDelta("a", d); err == nil {
		t.Fatal("wrong source accepted")
	}
}
func TestObservationEpochAndStatusSince(t *testing.T) {
	s, c, _ := open(t)
	put := func(status string) {
		if err := s.RecordSiteObservation(observation.Row{NodeID: "a", MonitorID: "m", Status: status, CheckedAt: c.t, IntervalMS: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	put("up")
	first, _ := s.SiteObservationDelta("a", "", 0)
	c.t = c.t.Add(time.Second)
	put("up")
	same, _ := s.SiteObservationDelta("a", "", 0)
	if !first.Rows[0].StatusSince.Equal(same.Rows[0].StatusSince) || same.Rows[0].Seq <= first.Rows[0].Seq {
		t.Fatal(first, same)
	}
	c.t = c.t.Add(time.Second)
	put("down")
	last, _ := s.SiteObservationDelta("a", "", 0)
	if !last.Rows[0].StatusSince.Equal(c.t) {
		t.Fatal(last)
	}
	oldEpoch := last.Epoch
	s.DeleteSetting("db_epoch")
	put("up")
	reset, _ := s.SiteObservationDelta("a", oldEpoch, last.Cursor)
	if reset.Epoch == oldEpoch || len(reset.Rows) != 1 {
		t.Fatal(reset)
	}
}

func TestNotificationObservationSnapshotIsDurable(t *testing.T) {
	s, c, _ := open(t)
	if err := s.RecordSiteObservation(observation.Row{NodeID: "a", MonitorID: "m", Status: "down", CheckedAt: c.t, IntervalMS: 1000}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.SiteObservationSnapshot("m", "", []string{"a", "b"}, c.t)
	if err != nil {
		t.Fatal(err)
	}
	in := &engine.Intent{ID: "problem", State: engine.IntentPending, CreatedAt: c.t, UpdatedAt: c.t, Message: notify.Message{Kind: "PROBLEM", Observations: snap}}
	if err := s.SaveIntent(in); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(time.Second)
	s.RecordSiteObservation(observation.Row{NodeID: "a", MonitorID: "m", Status: "up", CheckedAt: c.t, IntervalMS: 1000})
	recovery, _ := s.SiteObservationSnapshot("m", "", []string{"a", "b"}, c.t)
	if err := s.SaveIntent(&engine.Intent{ID: "recovery", State: engine.IntentPending, CreatedAt: c.t, UpdatedAt: c.t, Message: notify.Message{Kind: "RECOVERY", Observations: recovery}}); err != nil {
		t.Fatal(err)
	}
	snap.Rows[0].Status = "unknown" // even changing caller memory cannot alter persisted intents
	pending, err := s.PendingIntents()
	if err != nil || len(pending) != 2 {
		t.Fatal(pending, err)
	}
	for _, p := range pending {
		want := "down"
		if p.ID == "recovery" {
			want = "up"
		}
		if p.Message.Observations == nil || p.Message.Observations.Rows[0].Status != want {
			t.Fatal(p)
		}
	}
}

func TestObservationSourceClockSkew(t *testing.T) {
	src, sc, _ := open(t)
	dst, dc, _ := open(t)
	sc.t = dc.t.Add(24 * time.Hour)
	put := func(status string) {
		t.Helper()
		if err := src.RecordSiteObservation(observation.Row{NodeID: "a", MonitorID: "web", Status: status, CheckedAt: sc.t, IntervalMS: 1000, DefHash: "v1"}); err != nil {
			t.Fatal(err)
		}
	}
	put("up")
	d, err := src.SiteObservationDelta("a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = dst.ApplySiteObservationDelta("a", d); err != nil {
		t.Fatal(err)
	}
	snap, err := dst.SiteObservationSnapshot("web", "v1", []string{"a"}, dc.t)
	if err != nil || snap.Rows[0].Stale || !snap.Rows[0].ReceivedAt.Equal(dc.t) {
		t.Fatal(snap, err)
	}
	dc.t = dc.t.Add(4 * time.Second)
	snap, err = dst.SiteObservationSnapshot("web", "v1", []string{"a"}, dc.t)
	if err != nil || !snap.Rows[0].Stale {
		t.Fatal("future source timestamp concealed stale row", snap, err)
	}
	sc.t = sc.t.Add(-48 * time.Hour)
	put("down")
	next, err := src.SiteObservationDelta("a", d.Epoch, d.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if err = dst.ApplySiteObservationDelta("a", next); err != nil {
		t.Fatal(err)
	}
	snap, err = dst.SiteObservationSnapshot("web", "v1", []string{"a"}, dc.t)
	if err != nil || snap.Rows[0].Stale || snap.Rows[0].Status != "down" {
		t.Fatal("clock rollback must not outrank source sequence", snap, err)
	}
}
