package server

import (
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/license"
)

type auditItem struct {
	Actor, Action, Target, Area string
	Detail                      map[string]any
}

func TestAuditLog(t *testing.T) {
	admin := newEnv(t, usersBind, nil)
	admin.setup()
	var created struct{ ID int64 }
	if c := admin.do("POST", "/api/v1/users", map[string]any{"username": "bjops", "password": "secret123", "role": "viewer"}, &created); c != 200 {
		t.Fatalf("create: %d", c)
	}
	if c := admin.do("PUT", "/api/v1/users/"+itoa(created.ID), map[string]any{"role": "operator", "areas": []string{"region:north"}}, nil); c != 200 {
		t.Fatalf("update: %d", c)
	}
	ops := admin.loginAs("bjops", "secret123")
	if c := ops.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "bj-sw", "type": "PING", "target": "10.10.0.2"}}, nil); c != 200 {
		t.Fatalf("operator create: %d", c)
	}
	admin.do("POST", "/api/v1/session", map[string]string{"username": "bjops", "password": "nope-nope"}, nil)
	if c := ops.do("GET", "/api/v1/audit", nil, nil); c != 403 {
		t.Fatalf("operators may not read the audit log: %d", c)
	}
	// Recorded all along, readable once registered.
	if c := admin.do("GET", "/api/v1/audit", nil, nil); c != 402 {
		t.Fatalf("unregistered: %d", c)
	}
	admin.register(license.FeatAudit)

	var log struct{ Items []auditItem }
	admin.do("GET", "/api/v1/audit", nil, &log)
	find := func(action string) *auditItem {
		for i := range log.Items {
			if log.Items[i].Action == action {
				return &log.Items[i]
			}
		}
		t.Fatalf("no %s in %+v", action, log.Items)
		return nil
	}
	if e := find("user.create"); e.Target != "bjops" || e.Actor != "admin" || e.Detail["role"] != "viewer" {
		t.Fatalf("user.create: %+v", e)
	}
	e := find("user.update")
	before, _ := e.Detail["before"].(map[string]any)
	after, _ := e.Detail["after"].(map[string]any)
	if e.Target != "bjops" || before["role"] != "viewer" || after["role"] != "operator" {
		t.Fatalf("user.update: %+v", e)
	}
	if e := find("monitor.create"); e.Actor != "bjops" || e.Target != "bj-sw" || e.Area != "region:north" {
		t.Fatalf("monitor.create: %+v", e)
	}
	if e := find("config.apply"); e.Actor != "bjops" || e.Area != "region:north" {
		t.Fatalf("config.apply: %+v", e)
	}
	if e := find("session.login_failed"); e.Actor != "bjops" {
		t.Fatalf("login_failed: %+v", e)
	}
	// Filters.
	admin.do("GET", "/api/v1/audit?action=user", nil, &log)
	for _, e := range log.Items {
		if e.Action != "user.create" && e.Action != "user.update" {
			t.Fatalf("filter user: %+v", e)
		}
	}
	admin.do("GET", "/api/v1/audit?area=region:north&actor=bjops", nil, &log)
	if len(log.Items) != 2 {
		t.Fatalf("filter area+actor: %+v", log.Items)
	}
	// The configuration change carries the record before and after.
	admin.do("GET", "/api/v1/audit?action=config.apply&actor=bjops", nil, &log)
	diff, _ := log.Items[0].Detail["diff"].([]any)
	if len(diff) != 1 || diff[0].(map[string]any)["after"] == nil || !strings.Contains(diff[0].(map[string]any)["after"].(string), "10.10.0.2") {
		t.Fatalf("diff: %+v", log.Items[0].Detail)
	}
	// High-risk filter, verify, export.
	admin.do("GET", "/api/v1/audit?risk=high", nil, &log)
	for _, e := range log.Items {
		if e.Action == "monitor.create" {
			t.Fatalf("monitor.create is not high-risk: %+v", log.Items)
		}
	}
	var v struct {
		OK      bool
		Entries int
		Head    string
	}
	if c := admin.do("GET", "/api/v1/audit/verify", nil, &v); c != 200 || !v.OK || v.Entries < 5 || v.Head == "" {
		t.Fatalf("verify: %d %+v", c, v)
	}
	var rows []map[string]any
	if c := admin.do("GET", "/api/v1/audit/export?format=json&action=user", nil, &rows); c != 200 || len(rows) != 2 {
		t.Fatalf("export: %d %+v", c, rows)
	}
	ops.do("DELETE", "/api/v1/session", nil, nil)
	admin.do("GET", "/api/v1/audit?action=session.logout", nil, &log)
	if len(log.Items) != 1 || log.Items[0].Actor != "bjops" {
		t.Fatalf("logout: %+v", log.Items)
	}
	admin.do("GET", "/api/v1/audit?action=audit", nil, &log)
	if len(log.Items) != 1 || log.Items[0].Action != "audit.export" {
		t.Fatalf("the export itself is recorded: %+v", log.Items)
	}
}

func TestAuditActionNames(t *testing.T) {
	for pattern, want := range map[string]string{
		"POST /api/v1/users":                       "user.create",
		"PUT /api/v1/users/{id}":                   "user.update",
		"POST /api/v1/monitors/{name}/pause":       "monitor.pause",
		"DELETE /api/v1/nodes/{name}/token":        "node.token.delete",
		"POST /api/v1/incidents/{id}/ack":          "incident.ack",
		"PUT /api/v1/config":                       "config.update",
		"POST /api/v1/maintenance":                 "maintenance.create",
		"PUT /api/v1/secrets/{name}":               "secret.update",
		"POST /api/v1/cluster/join-codes":          "cluster.join-codes",
		"POST /api/v1/reports/daily/send":          "reports.daily.send",
		"DELETE /api/v1/incidents/{id}/ack":        "incident.ack.delete",
		"POST /api/v1/monitors/{name}/push/rotate": "monitor.push.rotate",
	} {
		if got := auditAction(pattern); got != want {
			t.Errorf("%s: %s, want %s", pattern, got, want)
		}
	}
}

// Two people share the admin account, each in their own browser: every
// entry says which sign-in and which browser it came from, the sign-in's
// changes can be listed on their own, and the chain still verifies.
func TestAuditTellsSharedAccountApart(t *testing.T) {
	a := newEnv(t, usersBind, nil)
	a.setup()
	b := a.loginAs("admin", "secret123")
	const chrome = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	const firefox = "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0"
	for _, x := range []struct {
		e        *env
		name, ua string
	}{{a, "from-chrome", chrome}, {b, "from-firefox", firefox}} {
		if c := x.e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": x.name, "type": "PING", "target": "10.0.0.9"}}, nil, "User-Agent", x.ua); c != 200 {
			t.Fatalf("%s: %d", x.name, c)
		}
	}
	a.register(license.FeatAudit)
	var log struct{ Items []auditItem }
	a.do("GET", "/api/v1/audit", nil, &log)
	client := func(target string) (session, ua string) {
		for _, it := range log.Items {
			if it.Action == "monitor.create" && it.Target == target {
				c, _ := it.Detail["client"].(map[string]any)
				session, _ = c["session"].(string)
				ua, _ = c["ua"].(string)
				return session, ua
			}
		}
		t.Fatalf("no monitor.create %s in %+v", target, log.Items)
		return
	}
	sa, ua := client("from-chrome")
	sb, ub := client("from-firefox")
	if sa == "" || sb == "" || sa == sb || ua != chrome || ub != firefox {
		t.Fatalf("sessions %q %q, browsers %q %q", sa, sb, ua, ub)
	}
	// The configuration change each one made carries the same sign-in.
	for rec, want := range map[string]string{"from-chrome": sa, "from-firefox": sb} {
		found := false
		for _, it := range log.Items {
			diff, _ := it.Detail["diff"].([]any)
			if it.Action != "config.apply" || len(diff) == 0 || diff[0].(map[string]any)["record"] != rec {
				continue
			}
			c, _ := it.Detail["client"].(map[string]any)
			if c["session"] != want {
				t.Fatalf("config.apply of %s: %+v", rec, it)
			}
			found = true
		}
		if !found {
			t.Fatalf("no config.apply for %s", rec)
		}
	}
	var only struct{ Items []auditItem }
	a.do("GET", "/api/v1/audit?session="+sb, nil, &only)
	login := false
	for _, it := range only.Items {
		c, _ := it.Detail["client"].(map[string]any)
		if c["session"] != sb {
			t.Fatalf("filtered by %s: %+v", sb, it)
		}
		login = login || it.Action == "session.login"
	}
	if !login || len(only.Items) < 2 {
		t.Fatalf("the second sign-in and its change: %+v", only.Items)
	}
	var check struct{ OK bool }
	if a.do("GET", "/api/v1/audit/verify", nil, &check); !check.OK {
		t.Fatal("chain")
	}
}

// The audit retention cannot be changed from the web, so the log cannot
// be cleared from there; the page shows the retention in effect.
func TestAuditRetentionNotFromTheWeb(t *testing.T) {
	e := newEnv(t, usersBind, nil)
	e.setup()
	for _, body := range []map[string]any{
		{"set": map[string]string{"retention.audit": "30d"}},
		{"unset": []string{"retention.audit"}},
	} {
		if c := e.do("PUT", "/api/v1/tunables", body, nil); c != 403 {
			t.Fatalf("%v: %d", body, c)
		}
	}
	var tun []struct{ Key, Value, Override string }
	e.do("GET", "/api/v1/tunables", nil, &tun)
	for _, x := range tun {
		if x.Key == "retention.audit" && (x.Value != "730d" || x.Override == "") {
			t.Fatalf("%+v", x)
		}
	}
}
