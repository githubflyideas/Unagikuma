package store

import "testing"

func TestCopyControlPreservesRuntime(t *testing.T) {
	src, _, _ := open(t)
	dst, _, _ := open(t)
	for _, q := range []string{
		`INSERT INTO people(id,handle,name,username,left_at,created_at,updated_at,notify) VALUES(7,'a','A','old',1,1,2,'sms,email')`,
		`INSERT INTO contacts(person_id,kind,value) VALUES(7,'email','a@example.com')`,
		`INSERT INTO oncall_overrides(rotation,member,from_at,until_at,created_at) VALUES('ops','a',1,2,1)`,
	} {
		if _, err := src.core.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := dst.SetSetting("test-runtime", "keep"); err != nil {
		t.Fatal(err)
	}
	if err := dst.Audit(AuditEntry{Actor: "site-b", Action: "test", Target: "local"}); err != nil {
		t.Fatal(err)
	}
	var before int
	dst.core.QueryRow("SELECT count(*) FROM audit").Scan(&before)
	if err := dst.CopyControlFrom(src, false); err != nil {
		t.Fatal(err)
	}
	people, err := dst.People()
	if err != nil || len(people) != 1 || people[0].Handle != "a" || !people[0].Left() || len(people[0].Notify) != 2 || len(people[0].Emails) != 1 {
		t.Fatalf("people=%+v err=%v", people, err)
	}
	var n int
	dst.core.QueryRow("SELECT count(*) FROM oncall_overrides").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	dst.core.QueryRow("SELECT count(*) FROM audit").Scan(&n)
	if n != before {
		t.Fatal("audit overwritten")
	}
	if err := dst.CopyControlFrom(src, false); err != nil {
		t.Fatal("not idempotent", err)
	}
}

func TestPromotionTransfersAgentAuthenticationOnlyAtTakeover(t *testing.T) {
	src, _, _ := open(t)
	dst, _, _ := open(t)
	token, err := src.CreateAgentToken("c")
	if err != nil {
		t.Fatal(err)
	}
	if err = dst.CopyControlFrom(src, false); err != nil {
		t.Fatal(err)
	}
	if _, err = dst.AgentNodeByToken(token, "127.0.0.1"); err == nil {
		t.Fatal("ordinary sync copied control authentication")
	}
	if err = dst.CopyControlFrom(src, true); err != nil {
		t.Fatal(err)
	}
	if node, err := dst.AgentNodeByToken(token, "127.0.0.1"); err != nil || node != "c" {
		t.Fatal(node, err)
	}
}
