package store

import (
	"github.com/githubflyideas/unagikuma/internal/engine"
	"testing"
	"time"
)

func TestIncidentIdentityAndEpochMerge(t *testing.T) {
	receiver, _, _ := open(t)
	keys := map[string]bool{}
	for _, name := range []string{"a", "b"} {
		src, c, _ := open(t)
		if err := src.SetSetting("source_node", name); err != nil {
			t.Fatal(err)
		}
		inc := &engine.Incident{Monitor: "web", Kind: "monitor"}
		inc.State.OpenedAt = time.Now()
		if err := src.SaveIncident(inc); err != nil {
			t.Fatal(err)
		}
		if inc.GlobalID == "" || keys[inc.GlobalID] {
			t.Fatal("identity collision", inc)
		}
		keys[inc.GlobalID] = true
		auditN(t, src, c, 2)
		epoch, _ := src.DBEpoch()
		tables, _, _ := src.ExportSince(map[string]int64{})
		if _, err := receiver.Merge(MergeBatch{Site: name, Epoch: epoch, Tables: tables}); err != nil {
			t.Fatal(err)
		}
		if n, err := receiver.VerifySiteAudit(MergeScope(name, epoch)); err != nil || n != 2 {
			t.Fatalf("source chain %d %v", n, err)
		}
		id, err := src.LocalIncidentID(inc.GlobalID)
		if err != nil || id != inc.ID {
			t.Fatalf("source resolve %d %v", id, err)
		}
		if id, _ := receiver.LocalIncidentID(inc.GlobalID); id != 0 {
			t.Fatal("import resolved as local")
		}
	}
	local, _ := receiver.Incidents("all", "", 0, 100)
	all, _ := receiver.Incidents("all", "", 0, 100, "*")
	if len(local.Items) != 0 || len(all.Items) != 2 {
		t.Fatalf("scope local=%+v all=%+v", local, all)
	}
	for _, r := range all.Items {
		if !r.HistoryOnly || r.GlobalID == "" {
			t.Fatal(r)
		}
	}
}
