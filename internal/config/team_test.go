package config

import (
	"errors"
	"strings"
	"testing"
)

// A team lists its members by user name; a person can be in several
// teams. Monitors join with team= (also through $DEFAULT); the rest are in
// the built-in admin team. Legacy role lists read as members.
func TestTeams(t *testing.T) {
	const head = "$FILE revision=1\nc IN CHANNEL log\nd IN CHANNEL log\nops IN POLICY chain=c\n"
	_, cfg, ds := load(t, head+`web  IN TEAM title="Web team" members=li,zhang channels=c,d mode=all tenant=t1
db   IN TEAM managers=li viewers=zhang
shop IN HTTP https://shop.example.com/ team=web
$DEFAULT team=db
pg   IN TCP 10.0.0.5:5432
$DEFAULT
gw   IN PING 10.0.0.1
adm  IN PING 10.0.0.2 team=admin
`)
	if e := errorsOf(ds); len(e) > 0 {
		t.Fatal(e)
	}
	web, db := cfg.FindTeam("web"), cfg.FindTeam("db")
	if web == nil || db == nil || web.Title != "Web team" || db.Title != "db" || web.Mode != "all" || strings.Join(web.Channels, ",") != "c,d" {
		t.Fatalf("teams: %+v %+v", web, db)
	}
	if !db.Legacy || !db.Has("li") || !db.Has("zhang") || web.Has("zhao") {
		t.Errorf("members: %+v", db)
	}
	if a := cfg.FindTeam(AdminTeam); a == nil || !a.Implicit {
		t.Errorf("admin team: %+v", a)
	}
	if !cfg.MultiTeam() || strings.Join(cfg.TeamsOf("li"), ",") != "db,web" && strings.Join(cfg.TeamsOf("li"), ",") != "web,db" {
		t.Errorf("teams of li: %v", cfg.TeamsOf("li"))
	}
	if got := cfg.TeamsOf("nobody"); len(got) != 1 || got[0] != AdminTeam {
		t.Errorf("teams of nobody: %v", got)
	}
	if m := cfg.FindMonitor("adm"); m.Team != "" || m.TeamOf() != AdminTeam {
		t.Errorf("team=admin: %+v", m.Team)
	}
	for name, team := range map[string]string{"shop": "web", "pg": "db", "gw": ""} {
		if m := cfg.FindMonitor(name); m.Team != team {
			t.Errorf("%s: team %q, want %q", name, m.Team, team)
		}
	}
	sel, _ := ParseSelector("team:web")
	if !sel.Match(cfg.FindMonitor("shop")) || sel.Match(cfg.FindMonitor("gw")) {
		t.Error("team: selector")
	}

	for src, code := range map[string]string{
		"x IN HTTP https://x/ team=nope\n":                  "unknown_team",
		"web IN TEAM channels=nope\n":                       "unknown_channel",
		"web IN TEAM mode=some\n":                           "invalid_value",
		"web IN TEAM managers=li operators=li\n":            "invalid_value",
		"web IN TEAM colour=red\n":                          "unknown_option",
		"web IN TEAM somewhere\n":                           "syntax",
		"web IN TEAM managers=li\nweb IN HTTP https://x/\n": "duplicate_name",
	} {
		_, _, ds := load(t, head+src)
		if !strings.Contains(errCodes(ds), code) {
			t.Errorf("%q: want %s, got %v", src, code, ds)
		}
	}
}

func errCodes(ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range errorsOf(ds) {
		b.WriteString(d.Code + " ")
	}
	return b.String()
}

// Saving a record never replaces one of another kind with the same name:
// a team or policy named like a monitor is refused, while a monitor may
// change its check type.
func TestUpsertKeepsOtherKinds(t *testing.T) {
	src := "$FILE revision=1\nc IN CHANNEL log\nweb IN HTTP https://example.com/\n"
	f, _ := Parse("x", []byte(src))
	for _, typ := range []string{"TEAM", "POLICY", "CHANNEL"} {
		err := f.UpsertRecord(RecordSpec{Name: "web", Type: typ, Opts: []Opt{{Key: "chain", Value: "c"}}})
		if !errors.Is(err, ErrNameTaken) {
			t.Errorf("%s named like a monitor: %v", typ, err)
		}
	}
	if string(f.Bytes()) != src {
		t.Fatalf("file changed:\n%s", f.Bytes())
	}
	if err := f.UpsertRecord(RecordSpec{Name: "web", Type: "TCP", Target: "example.com:443"}); err != nil {
		t.Fatalf("a monitor changing its type: %v", err)
	}
	if err := f.UpsertRecord(RecordSpec{Name: "c", Type: "channel", Target: "log"}); err != nil {
		t.Fatalf("same kind, other case: %v", err)
	}
}

// A team's monitors use the team's policy unless they name their own.
func TestTeamPolicy(t *testing.T) {
	_, cfg, ds := load(t, `$FILE revision=1
c IN CHANNEL log
web-oncall IN POLICY chain=c
db-oncall IN POLICY chain=c
web IN TEAM managers=li policy=web-oncall
db  IN TEAM managers=li
shop IN HTTP https://shop.example.com/ team=web
cart IN HTTP https://cart.example.com/ team=web policy=db-oncall
pg   IN TCP 10.0.0.5:5432 team=db
gw   IN PING 10.0.0.1
$DEFAULT policy=db-oncall
api  IN HTTP https://api.example.com/ team=web
`)
	if e := errorsOf(ds); len(e) > 0 {
		t.Fatal(e)
	}
	for name, want := range map[string]string{"shop": "web-oncall", "cart": "db-oncall", "pg": "default", "gw": "default", "api": "db-oncall"} {
		if got := cfg.FindMonitor(name).Policy; got != want {
			t.Errorf("%s: policy %s, want %s", name, got, want)
		}
	}
}
