package store

import (
	"testing"
	"time"
)

func auditN(t *testing.T, s *Store, c *clock, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		c.t = c.t.Add(time.Minute)
		if err := s.Audit(AuditEntry{Actor: "alice", Action: "monitor.update", Target: "web", Addr: "10.0.0.1",
			Detail: map[string]any{"before": "a", "after": "b", "i": i}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditChainVerifies(t *testing.T) {
	s, c, _ := open(t)
	auditN(t, s, c, 5)
	s.Audit(AuditEntry{Actor: "bob", Action: "user.delete", Target: "carol"})
	r, err := s.VerifyAudit()
	if err != nil || !r.OK || r.Entries != 6 || r.Head == "" {
		t.Fatalf("%v %+v", err, r)
	}
	items, _ := s.AuditLog(AuditQuery{Risk: "high"})
	if len(items) != 1 || items[0].Action != "user.delete" {
		t.Fatalf("high-risk filter: %+v", items)
	}
	if !s.AuditContains(r.Head) {
		t.Fatal("head not found")
	}
}

// Entries cannot be updated; an edit made around that (the trigger
// dropped), a removal or an insertion is found and located.
func TestAuditTamperDetected(t *testing.T) {
	s, c, _ := open(t)
	auditN(t, s, c, 6)
	if _, err := s.core.Exec(`UPDATE audit SET actor='mallory' WHERE id=3`); err == nil {
		t.Fatal("an audit entry could be updated")
	}
	if _, err := s.core.Exec(`DROP TRIGGER audit_append_only`); err != nil {
		t.Fatal(err)
	}
	s.core.Exec(`UPDATE audit SET actor='mallory' WHERE id=3`)
	r, _ := s.VerifyAudit()
	if r.OK || r.BadID != 3 {
		t.Fatalf("edit not found: %+v", r)
	}
	s.core.Exec(`UPDATE audit SET actor='alice' WHERE id=3`)
	if r, _ := s.VerifyAudit(); !r.OK {
		t.Fatalf("restored entry: %+v", r)
	}
	s.core.Exec(`DELETE FROM audit WHERE id=4`)
	if r, _ := s.VerifyAudit(); r.OK || r.BadID != 5 {
		t.Fatalf("removal not found: %+v", r)
	}
	s.core.Exec(`INSERT INTO audit(id,at,actor,action,prev,hash) SELECT 4,at,'x','user.create',prev,hash FROM audit WHERE id=3`)
	if r, _ := s.VerifyAudit(); r.OK {
		t.Fatalf("forged entry not found: %+v", r)
	}
}

// Old entries leave after the audit retention; the chain then starts at the
// anchor and still verifies, and new entries continue it.
func TestAuditPruneKeepsChain(t *testing.T) {
	s, c, _ := open(t)
	auditN(t, s, c, 4)
	before, _ := s.VerifyAudit()
	c.t = c.t.Add(10 * 24 * time.Hour)
	auditN(t, s, c, 2)
	s.SetAuditRetention(24 * time.Hour) // the server's --audit-retention; the configuration cannot shorten it
	if err := s.Prune(Retention{Raw: time.Hour, Agg5m: time.Hour, Agg1h: time.Hour, Events: time.Hour, Audit: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.VerifyAudit()
	if !r.OK || r.Entries != 2 {
		t.Fatalf("after prune: %+v", r)
	}
	if !s.AuditContains(before.Head) {
		t.Fatal("the last pruned hash is the anchor")
	}
	auditN(t, s, c, 1)
	if r, _ := s.VerifyAudit(); !r.OK || r.Entries != 3 {
		t.Fatalf("continued: %+v", r)
	}
}

// Entries written before the chain existed are chained at open.
func TestAuditLegacyChained(t *testing.T) {
	s, _, dir := open(t)
	s.core.Exec(`INSERT INTO audit(at,actor,action) VALUES(1,'old','config.apply'),(2,'old','user.create')`)
	s.Close()
	s2, err := Open(dir, Options{CommitInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if r, _ := s2.VerifyAudit(); !r.OK || r.Entries != 2 {
		t.Fatalf("%+v", r)
	}
}

// The configuration, which the web UI can edit, only ever lengthens the
// audit retention: shortening it takes the server's --audit-retention.
func TestAuditRetentionOnlyLengthens(t *testing.T) {
	s, c, _ := open(t)
	day := 24 * time.Hour
	if got := s.AuditRetention(30 * day); got != DefaultAuditRetention {
		t.Fatalf("30d configured, no flag: %v", got)
	}
	if got := s.AuditRetention(1000 * day); got != 1000*day {
		t.Fatalf("a longer configured retention: %v", got)
	}
	auditN(t, s, c, 3)
	c.t = c.t.Add(100 * day)
	if err := s.Prune(Retention{Audit: 30 * day}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.VerifyAudit(); r.Entries != 3 {
		t.Fatalf("30d from the configuration pruned the log: %+v", r)
	}
	s.SetAuditRetention(60 * day) // --audit-retention=60d
	if err := s.Prune(Retention{Audit: 30 * day}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.VerifyAudit(); r.Entries != 0 || !r.OK {
		t.Fatalf("the server's 60d: %+v", r)
	}
}

// The browser and sign-in an entry came from are part of the chained
// entry: changing them afterwards is found like any other change.
func TestAuditClientIsChained(t *testing.T) {
	s, _, _ := open(t)
	if err := s.Audit(AuditEntry{Actor: "admin", Action: "monitor.create", Target: "web",
		Detail: map[string]any{"client": map[string]any{"session": "1a2b3c4d", "ua": "Firefox/132"}}}); err != nil {
		t.Fatal(err)
	}
	items, _ := s.AuditLog(AuditQuery{Session: "1a2b3c4d"})
	if len(items) != 1 {
		t.Fatalf("session filter: %+v", items)
	}
	if sess, ua := items[0].Client(); sess != "1a2b3c4d" || ua != "Firefox/132" {
		t.Fatalf("client: %q %q", sess, ua)
	}
	if items, _ := s.AuditLog(AuditQuery{Session: "1a2b3c4_"}); len(items) != 0 {
		t.Fatalf("_ must not match as a wildcard: %+v", items)
	}
	if _, err := s.core.Exec(`DROP TRIGGER audit_append_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.core.Exec(`UPDATE audit SET detail=replace(detail,'Firefox','Chrome')`); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.VerifyAudit(); r.OK {
		t.Fatal("an edited browser went unnoticed")
	}
}
