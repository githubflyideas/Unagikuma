package server

import (
	"testing"

	"github.com/githubflyideas/unagikuma/internal/store"
)

func TestChannelTestHTTPUsesDirectoryWhitelist(t *testing.T) {
	bind := `@ IN SOA unagikuma. admin.localhost. ( 1 1m 15s 5s 3 )
mail IN CHANNEL email host=127.0.0.1 port=9 tls=none from=mon@example.com to="li@example.com, stranger@example.com"
`
	e := newEnv(t, bind, nil)
	e.setup()

	// First-start import may have added both addresses; mark stranger unknown by
	// removing any person that only holds stranger@example.com, and ensure li is active.
	people, err := e.app.Store().People()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		keep := false
		for _, em := range p.Emails {
			if store.NormalizeEmail(em) == "li@example.com" {
				keep = true
			}
		}
		if !keep {
			_ = e.app.Store().DeletePerson(p.ID)
		}
	}
	e.app.DirectoryChanged()

	var out map[string]any
	code := e.do("POST", "/api/v1/channels/test", map[string]any{"name": "mail"}, &out)
	if code != 200 {
		t.Fatalf("status %d %v", code, out)
	}
	if out["ok"] != false {
		t.Fatalf("expected failure when only filtered recipients remain: %v", out)
	}
	diag, _ := out["diagnostic"].(map[string]any)
	if diag == nil || diag["mode"] != "alert" {
		t.Fatalf("diagnostic %v", out["diagnostic"])
	}
	if kept, _ := diag["kept"].(float64); kept != 1 && kept != 0 {
		// li may still be kept if imported; stranger filtered. Either way must not be fake-success with stranger-only.
		t.Logf("kept=%v filtered=%v", diag["kept"], diag["filtered"])
	}
	errMsg, _ := out["error"].(string)
	if errMsg == "" && out["ok"] == true {
		t.Fatal("must not report success when whitelist emptied recipients")
	}

	// Connection-only mode may proceed without claiming whitelist delivery.
	out = map[string]any{}
	code = e.do("POST", "/api/v1/channels/test", map[string]any{"name": "mail", "mode": "connection"}, &out)
	if code != 200 {
		t.Fatalf("connection mode %d %v", code, out)
	}
	diag, _ = out["diagnostic"].(map[string]any)
	if diag["mode"] != "connection" {
		t.Fatalf("mode %v", diag)
	}
}
