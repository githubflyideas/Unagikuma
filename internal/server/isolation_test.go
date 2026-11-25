package server

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Two teams on one server besides the admin team: web (alice and carol,
// operators) and db (bob, a viewer, and carol). A team member
// sees and changes only what their teams own; anything else answers as if
// it did not exist.
const isolationBind = `$FILE revision=2026110401 interval=1s retry=500ms timeout=300ms attempts=1
$SET engine.first-round-delay=0s suspend.recheck-spread=0s api.login-rate=100/s
log     IN CHANNEL log
default IN POLICY chain=log
web     IN TEAM members=alice,carol
db      IN TEAM members=bob,carol
gw      IN TCP 10.0.0.1:1 title="Core gateway"
shop    IN TCP 10.0.0.2:443 team=web parent=gw
pg      IN TCP 10.0.0.3:1 team=db
pushy   IN PUSH period=1h team=db
`

func isolationEnv(t *testing.T) (admin, alice, bob, carol *env) {
	t.Helper()
	admin = newEnv(t, isolationBind, nil)
	admin.setup()
	for u, role := range map[string]string{"alice": "operator", "bob": "viewer", "carol": "operator"} {
		if c := admin.do("POST", "/api/v1/users", map[string]any{"username": u, "password": "secret123", "role": role}, nil); c != 200 {
			t.Fatalf("create %s: %d", u, c)
		}
	}
	// Wait for the failing monitors (port 1) to open their incidents.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var incs struct{ Items []struct{ Monitor string } }
		admin.do("GET", "/api/v1/incidents?state=all", nil, &incs)
		got := map[string]bool{}
		for _, i := range incs.Items {
			got[i.Monitor] = true
		}
		if got["gw"] && got["pg"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("incidents: %+v", incs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return admin, admin.loginAs("alice", "secret123"), admin.loginAs("bob", "secret123"), admin.loginAs("carol", "secret123")
}

func names(t *testing.T, e *env, path, field string) []string {
	t.Helper()
	var out struct{ Items []map[string]any }
	if c := e.do("GET", path, nil, &out); c != 200 {
		t.Fatalf("GET %s: %d", path, c)
	}
	var ns []string
	for _, it := range out.Items {
		if v, ok := it[field].(string); ok {
			ns = append(ns, v)
		}
	}
	return ns
}

func same(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	m := map[string]int{}
	for _, g := range got {
		m[g]++
	}
	for _, w := range want {
		if m[w] == 0 {
			return false
		}
		m[w]--
	}
	return true
}

func TestTeamIsolationReads(t *testing.T) {
	admin, alice, bob, carol := isolationEnv(t)
	for _, c := range []struct {
		who  *env
		want []string
	}{
		{admin, []string{"gw", "shop", "pg", "pushy"}},
		{alice, []string{"shop"}},
		{bob, []string{"pg", "pushy"}},
		{carol, []string{"shop", "pg", "pushy"}},
	} {
		if got := names(t, c.who, "/api/v1/monitors", "name"); !same(got, c.want...) {
			t.Errorf("monitors: %v, want %v", got, c.want)
		}
	}
	// Incidents and events: alice has none of her own (shop is fine).
	if got := names(t, alice, "/api/v1/incidents?state=all", "monitor"); len(got) != 0 {
		t.Errorf("alice incidents: %v", got)
	}
	for _, st := range []string{"all", "unhandled"} {
		if got := names(t, bob, "/api/v1/incidents?state="+st, "monitor"); !same(got, "pg") {
			t.Errorf("bob incidents %s: %v", st, got)
		}
	}
	for _, m := range names(t, bob, "/api/v1/events?limit=500", "monitor") {
		if m != "pg" && m != "pushy" {
			t.Errorf("bob sees an event of %q", m)
		}
	}
	// Details, history and tokens of other teams' monitors do not exist.
	for _, p := range []string{"/api/v1/monitors/pg", "/api/v1/monitors/gw", "/api/v1/monitors/pg/latency", "/api/v1/monitors/pg/timeline",
		"/api/v1/monitors/pushy/push", "/api/v1/monitors/nope"} {
		if c := alice.do("GET", p, nil, nil); c != 404 {
			t.Errorf("alice GET %s: %d", p, c)
		}
	}
	// What hers depends on: its state only.
	var det struct {
		ParentStates []struct {
			Name, Status string
			Open         bool
		} `json:"parent_states"`
	}
	if c := alice.do("GET", "/api/v1/monitors/shop", nil, &det); c != 200 || len(det.ParentStates) != 1 || det.ParentStates[0].Name != "gw" || det.ParentStates[0].Open {
		t.Errorf("shop parents: %d %+v", c, det)
	}
	// Heartbeats, hosts, reports, expiry.
	var hb struct{ Items map[string]any }
	alice.do("GET", "/api/v1/monitors/heartbeats?names=shop,pg,gw", nil, &hb)
	if _, ok := hb.Items["pg"]; ok || len(hb.Items) != 1 {
		t.Errorf("heartbeats: %v", hb.Items)
	}
	if got := names(t, alice, "/api/v1/reports/availability", "name"); !same(got, "shop") {
		t.Errorf("availability: %v", got)
	}
	var hosts struct{ Items []struct{ Monitors []string } }
	alice.do("GET", "/api/v1/hosts", nil, &hosts)
	if len(hosts.Items) != 1 || !same(hosts.Items[0].Monitors, "shop") {
		t.Errorf("hosts: %+v", hosts.Items)
	}
	// Overview: her counts only, nothing about channels or people.
	var ov struct {
		Counts    map[string]int
		Unhandled int
		Channels  []any
		Team      bool
	}
	alice.do("GET", "/api/v1/overview", nil, &ov)
	if ov.Counts["total"] != 1 || ov.Unhandled != 0 || len(ov.Channels) != 0 || !ov.Team {
		t.Errorf("alice overview: %+v", ov)
	}
	bob.do("GET", "/api/v1/overview", nil, &ov)
	if ov.Counts["total"] != 2 || ov.Unhandled != 1 {
		t.Errorf("bob overview: %+v", ov)
	}
	// Teams: their own; the session says which and with what role.
	if got := names(t, carol, "/api/v1/teams", "name"); !same(got, "web", "db") {
		t.Errorf("carol teams: %v", got)
	}
	if got := names(t, alice, "/api/v1/teams", "name"); !same(got, "web") {
		t.Errorf("alice teams: %v", got)
	}
	var sess struct{ Teams map[string]string }
	carol.do("GET", "/api/v1/session", nil, &sess)
	if sess.Teams["web"] != "operator" || sess.Teams["db"] != "operator" {
		t.Errorf("carol session: %+v", sess)
	}
}

func TestTeamIsolationWrites(t *testing.T) {
	_, alice, bob, carol := isolationEnv(t)
	put := func(e *env, name, team string) int {
		opts := map[string]string{}
		if team != "" {
			opts["team"] = team
		}
		return e.do("PUT", "/api/v1/monitors/"+name, map[string]any{"record": map[string]any{"type": "TCP", "target": "10.0.0.9:22", "opts": opts}}, nil)
	}
	// Other teams' monitors do not exist for her.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PUT", "/api/v1/monitors/pg", map[string]any{"record": map[string]any{"type": "TCP", "target": "10.0.0.9:22"}}},
		{"DELETE", "/api/v1/monitors/pg", nil},
		{"POST", "/api/v1/monitors/pg/pause", map[string]any{"paused": true}},
		{"POST", "/api/v1/monitors/pg/check", nil},
		{"POST", "/api/v1/monitors/bulk-delete", map[string]any{"names": []string{"pg"}}},
		{"POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"gw"}, "paused": true}},
		{"POST", "/api/v1/monitors/pushy/push/rotate", nil},
	} {
		if code := alice.do(c.method, c.path, c.body, nil); code != 404 {
			t.Errorf("alice %s %s: %d", c.method, c.path, code)
		}
	}
	// Acknowledging another team's incident: not found either.
	var incs struct{ Items []struct{ ID int64 } }
	bob.do("GET", "/api/v1/incidents?state=unhandled", nil, &incs)
	if len(incs.Items) != 1 {
		t.Fatalf("bob's incident: %+v", incs)
	}
	id := incs.Items[0].ID
	if c := alice.do("POST", "/api/v1/incidents/"+strconv.FormatInt(id, 10)+"/ack", map[string]any{}, nil); c != 404 {
		t.Errorf("alice acks pg: %d", c)
	}
	if c := alice.do("GET", "/api/v1/incidents/"+strconv.FormatInt(id, 10)+"/report", nil, nil); c != 404 {
		t.Errorf("alice reads pg's report: %d", c)
	}
	// A viewer reads but does not change or acknowledge.
	if c := bob.do("GET", "/api/v1/incidents/"+strconv.FormatInt(id, 10)+"/report", nil, nil); c != 200 {
		t.Errorf("bob reads his report: %d", c)
	}
	if c := bob.do("POST", "/api/v1/incidents/"+strconv.FormatInt(id, 10)+"/ack", map[string]any{}, nil); c != 403 {
		t.Errorf("bob (viewer) acks: %d", c)
	}
	if c := put(bob, "pg", "db"); c != 403 {
		t.Errorf("bob (viewer) changes pg: %d", c)
	}
	// carol operates both teams: she moves a monitor between them, and
	// must say which when saving without team=.
	if c := put(carol, "pg", "db"); c != 200 {
		t.Errorf("carol changes a db monitor: %d", c)
	}
	if c := put(carol, "shop", "db"); c != 200 {
		t.Errorf("carol moves shop to db: %d", c)
	}
	if c := put(carol, "shop", ""); c != 403 {
		t.Errorf("carol saves shop without team=: %d", c)
	}
	if c := put(carol, "shop", "admin"); c != 403 {
		t.Errorf("carol moves shop to the admin team: %d", c)
	}
	if c := put(carol, "shop", "web"); c != 200 {
		t.Errorf("carol moves shop back: %d", c)
	}
	// alice has one team: team= is filled in.
	if c := put(alice, "shop", ""); c != 200 {
		t.Errorf("alice saves shop without team= (kept in her only team): %d", c)
	}
	if m := alice.app.Config().Config.FindMonitor("shop"); m == nil || m.Team != "web" {
		t.Errorf("shop left web: %+v", m)
	}
	// New monitors go into her team; another team is refused.
	create := func(e *env, name, team string) int {
		opts := map[string]string{}
		if team != "" {
			opts["team"] = team
		}
		return e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": name, "type": "TCP", "target": "10.0.0.8:22", "opts": opts}}, nil)
	}
	if c := create(alice, "new1", ""); c != 200 {
		t.Errorf("alice creates: %d", c)
	}
	if m := alice.app.Config().Config.FindMonitor("new1"); m == nil || m.Team != "web" {
		t.Errorf("new1: %+v", m)
	}
	if c := create(alice, "new2", "db"); c != 403 {
		t.Errorf("alice creates in db: %d", c)
	}
	// Maintenance only over her own monitors.
	maint := func(sel string) int {
		return alice.do("POST", "/api/v1/maintenance", map[string]any{"name": "m-" + strings.ReplaceAll(sel, ":", "-"), "title": "x", "select": sel, "at": "2030-01-01 00:00", "duration": "1h"}, nil)
	}
	if c := maint("name:pg"); c != 403 {
		t.Errorf("maintenance over pg: %d", c)
	}
	if c := maint("type:TCP"); c != 403 {
		t.Errorf("maintenance over everyone's TCP: %d", c)
	}
	if c := maint("name:shop"); c != 200 {
		t.Errorf("maintenance over shop: %d", c)
	}
	if got := names(t, bob, "/api/v1/maintenance", "name"); len(got) != 0 {
		t.Errorf("bob sees alice's window: %v", got)
	}
}

// Every API route a team member may call is reviewed for team isolation
// and listed in teamRoutes; all others refuse them, including routes
// added later.
func TestTeamRoutesDenyByDefault(t *testing.T) {
	admin, alice, _, _ := isolationEnv(t)
	subst := strings.NewReplacer("{name}", "x", "{id}", "1", "{source}", "kuma", "{lang}", "en")
	checked := 0
	for _, rt := range admin.srv.routeList {
		if rt.scope == "" || teamRoutes[rt.pattern] {
			continue
		}
		method, path, _ := strings.Cut(rt.pattern, " ")
		var out struct{ Code string }
		if c := alice.do(method, subst.Replace(path), map[string]any{}, &out); c != 403 {
			t.Errorf("%s: %d %s", rt.pattern, c, out.Code)
		}
		checked++
	}
	if checked < 50 {
		t.Fatalf("only %d routes checked", checked)
	}
	for p := range teamRoutes {
		found := false
		for _, rt := range admin.srv.routeList {
			found = found || rt.pattern == p
		}
		if !found {
			t.Errorf("teamRoutes lists %q, which is not a route", p)
		}
	}
}

// The live stream leaves out other teams' monitors and strips events that
// are about no monitor down to their type.
func TestTeamStream(t *testing.T) {
	admin, _, _, _ := isolationEnv(t)
	f := admin.srv.teamStream("alice", "operator")
	for _, c := range []struct {
		ev   sseEvent
		keep bool
		data string
	}{
		{sseEvent{typ: "state", monitor: "shop", data: []byte(`{"name":"shop"}`)}, true, `{"name":"shop"}`},
		{sseEvent{typ: "state", monitor: "pg", data: []byte(`{"name":"pg"}`)}, false, ""},
		{sseEvent{typ: "incident", monitor: "gw", data: []byte(`{"monitor":"gw"}`)}, false, ""},
		{sseEvent{typ: "serial", data: []byte(`{"serial":7,"author":"admin"}`)}, true, "{}"},
	} {
		got, keep := f(c.ev)
		if keep != c.keep || keep && string(got.data) != c.data {
			t.Errorf("%s %s: keep %v data %s", c.ev.typ, c.ev.monitor, keep, got.data)
		}
	}
	// An administrator sees everything; someone in no team is in the
	// admin team.
	if _, keep := admin.srv.teamStream("root", "admin")(sseEvent{typ: "state", monitor: "pg"}); !keep {
		t.Error("an administrator lost an event")
	}
	nobody := admin.srv.teamStream("nobody", "operator")
	if _, keep := nobody(sseEvent{typ: "state", monitor: "gw"}); !keep {
		t.Error("a user in no team lost an admin team event")
	}
	if _, keep := nobody(sseEvent{typ: "state", monitor: "pg"}); keep {
		t.Error("a user in no team sees the db team")
	}
}

// A team member's incident report lists only the configuration changes
// that touched their monitor, not what was changed elsewhere.
func TestTeamReportChanges(t *testing.T) {
	admin, _, bob, _ := isolationEnv(t)
	if c := admin.do("PUT", "/api/v1/monitors/gw", map[string]any{"record": map[string]any{"type": "TCP", "target": "10.0.0.1:1", "opts": map[string]string{"title": "Gateway renamed"}}}, nil); c != 200 {
		t.Fatalf("change gw: %d", c)
	}
	if c := admin.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "pg2", "type": "TCP", "target": "10.0.0.4:1", "opts": map[string]string{"team": "db"}}}, nil); c != 200 {
		t.Fatalf("create pg2: %d", c)
	}
	var id int64
	deadline := time.Now().Add(10 * time.Second)
	for id == 0 {
		var incs struct {
			Items []struct {
				ID      int64
				Monitor string
			}
		}
		bob.do("GET", "/api/v1/incidents?state=all", nil, &incs)
		for _, i := range incs.Items {
			if i.Monitor == "pg2" {
				id = i.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no incident for pg2")
		}
		time.Sleep(100 * time.Millisecond)
	}
	report := func(e *env) string {
		var rep struct {
			Changes []struct{ Target string } `json:"config_changes"`
		}
		if c := e.do("GET", "/api/v1/incidents/"+strconv.FormatInt(id, 10)+"/report", nil, &rep); c != 200 {
			t.Fatalf("report: %d", c)
		}
		var ts []string
		for _, c := range rep.Changes {
			ts = append(ts, c.Target)
		}
		return strings.Join(ts, ",")
	}
	// The administrator sees the whole lead-up; bob none of what touched
	// other monitors (or users, or the file as a whole).
	if got := report(admin); !strings.Contains(got, "gw") {
		t.Errorf("admin's report changes: %q", got)
	}
	for _, other := range []string{"gw", "alice", "monitors.conf"} {
		if got := report(bob); strings.Contains(got, other) {
			t.Errorf("bob's report shows a change to %s: %q", other, got)
		}
	}
}
