package store

import (
	"encoding/json"
	"testing"
)

// A site's incidents, events, decisions and audit merge into the master
// once: a second merge of the same rows adds nothing, decisions point at
// the merged incident, and the site's audit chain still verifies.
func TestMergeOnceAndChained(t *testing.T) {
	site, c, _ := open(t)
	master, _, _ := open(t)
	master.core.Exec(`INSERT INTO incidents(monitor,kind,state,opened_at,updated_at) VALUES('own','monitor','open',1,1)`)
	auditN(t, site, c, 3)
	site.core.Exec(`INSERT INTO incidents(monitor,kind,state,opened_at,updated_at) VALUES('flaky','monitor','open',5,5)`)
	site.core.Exec(`INSERT INTO decisions(at,incident_id,monitor,step,data) VALUES(6,1,'flaky','send','{}')`)
	site.core.Exec(`INSERT INTO events(at,type,monitor,message) VALUES(6,'problem','flaky','down')`)

	push := func() MergeResult {
		marks, err := master.MergeMarks("site-b")
		if err != nil {
			t.Fatal(err)
		}
		tables, _, err := site.ExportSince(marks)
		if err != nil {
			t.Fatal(err)
		}
		// over the wire as JSON
		raw, _ := json.Marshal(MergeBatch{Site: "site-b", Term: 2, Tables: tables})
		var b MergeBatch
		json.Unmarshal(raw, &b)
		r, err := master.Merge(b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := push()
	if r.Added["incidents"] != 1 || r.Added["decisions"] != 1 || r.Added["events"] != 1 || r.Added["audit"] != 3 {
		t.Fatalf("first merge: %+v", r)
	}
	if r = push(); len(r.Added) != 0 {
		t.Fatalf("second merge added %+v", r.Added)
	}
	var n, inc int64
	var siteTag string
	master.core.QueryRow(`SELECT count(*) FROM incidents`).Scan(&n)
	master.core.QueryRow(`SELECT d.incident_id, i.site FROM decisions d JOIN incidents i ON i.id=d.incident_id`).Scan(&inc, &siteTag)
	if n != 2 || inc != 2 || siteTag != "site-b" {
		t.Fatalf("incidents %d, decision → %d (%q)", n, inc, siteTag)
	}
	if k, err := master.VerifySiteAudit("site-b"); err != nil || k != 3 {
		t.Fatalf("site chain: %d %v", k, err)
	}
	if v, _ := master.VerifyAudit(); !v.OK {
		t.Fatalf("master chain broken: %+v", v)
	}
	// Records merged from elsewhere are not sent on again.
	if tables, _, _ := master.ExportSince(map[string]int64{}); len(tables["incidents"]) != 1 {
		t.Fatalf("re-export: %+v", tables["incidents"])
	}
	// Later rows go too.
	auditN(t, site, c, 1)
	if r = push(); r.Added["audit"] != 1 {
		t.Fatalf("third merge: %+v", r)
	}
	if k, err := master.VerifySiteAudit("site-b"); err != nil || k != 4 {
		t.Fatalf("site chain: %d %v", k, err)
	}
}
