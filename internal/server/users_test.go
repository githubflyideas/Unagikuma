package server

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

// loginAs returns an env acting as another user (own cookies and CSRF).
func (e *env) loginAs(user, pass string) *env {
	e.t.Helper()
	jar, _ := cookiejar.New(nil)
	o := &env{t: e.t, app: e.app, srv: e.srv, ts: e.ts, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	var r struct{ CSRF string }
	if c := o.do("POST", "/api/v1/session", map[string]string{"username": user, "password": pass}, &r); c != 200 {
		e.t.Fatalf("login %s: %d", user, c)
	}
	o.csrf = r.CSRF
	return o
}

const usersBind = `@ IN SOA unagikuma. admin.localhost. ( 2026102801 1m 15s 5s 2 )
$SET api.login-rate=100/s
bj  IN NODE region=north
web IN HTTP http://192.168.1.10/
$SCOPE region:north
bj-core IN PING 10.10.0.1
$SCOPE *
`

func TestRolesAndSections(t *testing.T) {
	admin := newEnv(t, usersBind, nil)
	admin.setup()
	for _, u := range []map[string]any{
		{"username": "bjops", "password": "secret123", "role": "operator", "areas": []string{"region:north"}},
		{"username": "watch", "password": "secret123", "role": "viewer", "areas": []string{"ignored"}},
	} {
		if c := admin.do("POST", "/api/v1/users", u, nil); c != 200 {
			t.Fatalf("create %v: %d", u["username"], c)
		}
	}
	var users struct {
		Items []struct {
			ID    int64    `json:"id"`
			Name  string   `json:"username"`
			Role  string   `json:"role"`
			Areas []string `json:"areas"`
		} `json:"items"`
	}
	admin.do("GET", "/api/v1/users", nil, &users)
	if len(users.Items) != 3 || users.Items[2].Name != "watch" || len(users.Items[2].Areas) != 0 {
		t.Fatalf("users: %+v", users.Items)
	}

	watch := admin.loginAs("watch", "secret123")
	if watch.do("GET", "/api/v1/monitors", nil, nil) != 200 || watch.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "x", "type": "PING", "target": "10.0.0.1"}}, nil) != 403 {
		t.Fatal("a viewer reads and does not write")
	}

	ops := admin.loginAs("bjops", "secret123")
	var sess struct {
		Role  string   `json:"role"`
		Areas []string `json:"areas"`
	}
	ops.do("GET", "/api/v1/session", nil, &sess)
	if sess.Role != "operator" || len(sess.Areas) != 1 {
		t.Fatalf("session: %+v", sess)
	}
	// New monitors land in the operator's section.
	if c := ops.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "bj-sw", "type": "PING", "target": "10.10.0.2"}}, nil); c != 200 {
		t.Fatalf("create in own section: %d", c)
	}
	if m := ops.app.Config().Config.FindMonitor("bj-sw"); m == nil || m.Area != "region:north" {
		t.Fatalf("placed in: %+v", m)
	}
	global := ""
	for code, req := range map[int][3]any{
		403: {"POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "g", "type": "PING", "target": "10.0.0.9", "area": global}}},
		200: {"PUT", "/api/v1/monitors/bj-core", map[string]any{"record": map[string]any{"type": "PING", "target": "10.10.0.3"}}},
	} {
		if c := ops.do(req[0].(string), req[1].(string), req[2], nil); c != code {
			t.Fatalf("%v: %d, want %d", req[1], c, code)
		}
	}
	for _, req := range [][3]any{
		{"PUT", "/api/v1/monitors/web", map[string]any{"record": map[string]any{"type": "HTTP", "target": "http://192.168.1.11/"}}},
		{"DELETE", "/api/v1/monitors/web", nil},
		{"POST", "/api/v1/monitors/web/pause", map[string]any{"paused": true}},
		{"PUT", "/api/v1/config", map[string]any{"text": "x"}},
		{"POST", "/api/v1/channels", map[string]any{"name": "c", "kind": "log"}},
		{"POST", "/api/v1/maintenance", map[string]any{"name": "m", "select": "name:web", "at": "2026-11-01 22:00", "duration": "1h"}},
		{"GET", "/api/v1/users", nil},
		{"POST", "/api/v1/backup", map[string]any{"passphrase": "long enough"}},
	} {
		if c := ops.do(req[0].(string), req[1].(string), req[2], nil); c != 403 {
			t.Fatalf("%s %s by a section operator: %d", req[0], req[1], c)
		}
	}
	if c := ops.do("POST", "/api/v1/maintenance", map[string]any{"name": "m", "select": "area:region:north", "at": "2026-11-01 22:00", "duration": "1h"}, nil); c != 200 {
		t.Fatalf("maintenance in own section: %d", c)
	}

	// The last administrator stays one; nobody deletes themselves.
	adminID := users.Items[0].ID
	if c := admin.do("PUT", "/api/v1/users/"+itoa(adminID), map[string]any{"role": "viewer"}, nil); c != 409 {
		t.Fatalf("demote last admin: %d", c)
	}
	if c := admin.do("DELETE", "/api/v1/users/"+itoa(adminID), nil, nil); c != 409 {
		t.Fatalf("delete self: %d", c)
	}
	// A role change ends the user's sessions.
	if c := admin.do("PUT", "/api/v1/users/"+itoa(users.Items[1].ID), map[string]any{"role": "viewer"}, nil); c != 200 {
		t.Fatalf("change role: %d", c)
	}
	if c := ops.do("GET", "/api/v1/monitors", nil, nil); c != 401 {
		t.Fatalf("old session after role change: %d", c)
	}
}

func itoa(n int64) string { return fmt.Sprint(n) }

func TestNoRecordInjectionThroughTheAPI(t *testing.T) {
	admin := newEnv(t, usersBind, nil)
	admin.setup()
	admin.do("POST", "/api/v1/users", map[string]any{"username": "bjops", "password": "secret123", "role": "operator", "areas": []string{"region:north"}}, nil)
	ops := admin.loginAs("bjops", "secret123")
	before := admin.app.Config().Text
	for _, rec := range []map[string]any{
		{"name": "x", "type": "PING", "target": "10.0.0.1\n$SCOPE *\nevil IN HTTP http://10.0.0.2/"},
		{"name": "x", "type": "PING", "target": "10.0.0.1", "interval": "1m\n$SET notify.repeat=1s"},
		{"name": "x", "type": "PING", "target": "10.0.0.1", "opts": map[string]string{"title": "t\nevil IN NODE"}},
	} {
		if c := ops.do("POST", "/api/v1/monitors", map[string]any{"record": rec}, nil); c != 400 {
			t.Fatalf("injection %v: %d", rec, c)
		}
	}
	if admin.app.Config().Text != before {
		t.Fatal("configuration changed")
	}
}
