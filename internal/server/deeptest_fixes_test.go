package server

import (
	"strings"
	"testing"
)

// Every signed-in person changes their own password, whatever their
// role; an API token, which is no person, cannot.
func TestOwnPasswordForEveryRole(t *testing.T) {
	admin := newEnv(t, strings.Replace(testBind, "$SET ", "$SET api.login-rate=100/s ", 1), nil)
	admin.setup()
	for _, role := range []string{"operator", "viewer"} {
		if c := admin.do("POST", "/api/v1/users", map[string]any{"username": role + "1", "password": "secret123", "role": role}, nil); c != 200 {
			t.Fatalf("create %s: %d", role, c)
		}
		u := admin.loginAs(role+"1", "secret123")
		if c := u.do("PUT", "/api/v1/session/password", map[string]string{"old": "secret123", "new": "newsecret456"}, nil); c != 200 {
			t.Fatalf("%s changing their own password: %d", role, c)
		}
		admin.loginAs(role+"1", "newsecret456") // fails the test if the new one does not work
	}
	var tok struct{ Token string }
	admin.do("POST", "/api/v1/tokens", map[string]any{"name": "t", "scopes": []string{"admin"}}, &tok)
	bearer := &env{t: t, app: admin.app, srv: admin.srv, ts: admin.ts, c: admin.ts.Client()}
	if c := bearer.do("PUT", "/api/v1/session/password", map[string]string{"old": "secret123", "new": "newsecret456"}, nil, "Authorization", "Bearer "+tok.Token); c != 403 {
		t.Fatalf("an API token changed a password: %d", c)
	}
}

// A secret still referred to by the configuration is not deleted: the
// file would stop validating and every later edit would be refused.
func TestSecretInUseIsNotDeleted(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	if c := e.do("PUT", "/api/v1/secrets/hook", map[string]string{"value": "https://hooks.example/x"}, nil); c != 204 {
		t.Fatalf("put secret: %d", c)
	}
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "wh", "kind": "webhook", "opts": map[string]string{"url": "${secret:hook}"}}, nil); c != 200 {
		t.Fatalf("channel: %d", c)
	}
	var er struct {
		Code   string
		UsedBy []string `json:"used_by"`
	}
	if c := e.do("DELETE", "/api/v1/secrets/hook", nil, &er); c != 409 || len(er.UsedBy) != 1 || er.UsedBy[0] != "wh" {
		t.Fatalf("delete a secret in use: %d %+v", c, er)
	}
	// Configuration edits keep working.
	if c := e.do("PUT", "/api/v1/tunables", map[string]any{"set": map[string]string{"notify.max": "5"}}, nil); c != 200 {
		t.Fatalf("edit after a refused delete: %d", c)
	}
	// Once nothing uses it, it goes.
	if c := e.do("DELETE", "/api/v1/channels/wh", nil, nil); c != 200 {
		t.Fatalf("delete channel: %d", c)
	}
	if c := e.do("DELETE", "/api/v1/secrets/hook", nil, nil); c != 204 {
		t.Fatalf("delete an unused secret: %d", c)
	}
}

func TestSummaryRejectsUnknownRange(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	if c := e.do("GET", "/api/v1/reports/summary?range=13d", nil, nil); c != 400 {
		t.Fatalf("range=13d: %d", c)
	}
	var s struct{ Range string }
	if c := e.do("GET", "/api/v1/reports/summary", nil, &s); c != 200 || s.Range != "30d" {
		t.Fatalf("no range: %d %q", c, s.Range)
	}
}

func TestTelegramTokenCheckedOnSave(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	var er struct{ Message string }
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "tg", "kind": "telegram", "opts": map[string]string{"token": "nocolon", "chat": "1"}}, &er); c != 400 || !strings.Contains(er.Message, "Telegram") {
		t.Fatalf("bad token saved: %d %q", c, er.Message)
	}
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "tg", "kind": "telegram", "opts": map[string]string{"token": "123456:AAE-x_y", "chat": "1"}}, nil); c != 200 {
		t.Fatalf("good token refused: %d", c)
	}
}
