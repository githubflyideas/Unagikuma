package app

import (
	"context"
	"errors"
	"github.com/githubflyideas/unagikuma/internal/store"
	"strings"
	"testing"
	"time"
)

func TestCrossSiteActionReturnsToSource(t *testing.T) {
	makeSite := func(name, other string) *App {
		text := strings.Replace(testConfig, "SOA unagikuma.", "SOA "+name+".", 1) + other + " IN NODE other\n"
		a, fp, _, _ := newApp(t, text)
		fp.set("web", true)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done })
		waitFor(t, "local incident", 6*time.Second, func() bool { r, ok := a.Engine().Row("web"); return ok && r.IncidentID != 0 })
		return a
	}
	b, c := makeSite("b", "c"), makeSite("c", "b")
	bs, cs := observationServer(t, b), observationServer(t, c)
	rememberObservationPeer(t, c, b, bs)
	rememberObservationPeer(t, b, c, cs)
	b.Store().Flush()
	tables, _, err := b.Store().ExportSince(map[string]int64{})
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := b.Store().DBEpoch()
	if _, err = c.Store().Merge(store.MergeBatch{Site: "b", Epoch: epoch, Tables: tables}); err != nil {
		t.Fatal(err)
	}
	imported, err := c.Store().Incidents("all", "web", 0, 100, "b")
	if err != nil || len(imported.Items) != 1 {
		t.Fatalf("imports %+v %v", imported, err)
	}
	row := imported.Items[0]
	if row.GlobalID == "" || !row.HistoryOnly {
		t.Fatal(row)
	}
	if err = c.ActOnIncident(context.Background(), row.ID, IncidentAction{Action: "ack", Actor: "alice", Comment: "from c"}, "web"); err != nil {
		t.Fatal(err)
	}
	br, _ := b.Engine().Row("web")
	cr, _ := c.Engine().Row("web")
	binc, _ := b.Store().Incident(br.IncidentID)
	cinc, _ := c.Store().Incident(cr.IncidentID)
	if !binc.State.Acked || cinc.State.Acked {
		t.Fatalf("wrong incident ACK: b=%+v c=%+v", binc, cinc)
	}
	trail, _ := b.Store().Acks(br.IncidentID)
	if len(trail) != 1 || trail[0].By != "alice" || trail[0].Via != "peer:c" {
		t.Fatal(trail)
	}
	if err = c.ActOnIncident(context.Background(), row.ID, IncidentAction{Action: "unack", Actor: "alice"}, "web"); err != nil {
		t.Fatal(err)
	}
	bs.Close()
	if err = c.ActOnIncident(context.Background(), row.ID, IncidentAction{Action: "ack", Actor: "alice"}, "web"); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("no unavailable: %v", err)
	}
	if v, _ := b.Store().VerifyAudit(); !v.OK {
		t.Fatal("source audit broken", v)
	}
}
