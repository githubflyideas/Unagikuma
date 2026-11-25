package config

import (
	"strings"
	"testing"
)

const scopeSrc = `@ IN SOA master. ops.example.com. ( 2026102801 1m 15s 5s 3 )
ops    IN CHANNEL log
bjops  IN POLICY chain=ops
bj     IN NODE "北京" region=north
tj     IN NODE "天津" region=north
sh     IN NODE "上海" region=east
www    IN HTTP https://www.example.com/

$SCOPE region:north policy=bjops
bj-core IN PING 10.10.0.1
bj-erp  IN HTTP http://10.10.1.5/ policy=default
bj-one  IN PING 10.10.0.2 nodes=bj

$SCOPE sh
sh-core IN PING 10.20.0.1

$SCOPE *
dns    IN DNS 223.5.5.5
`

func TestScopeSections(t *testing.T) {
	_, cfg, diags := Load("t", []byte(scopeSrc), Options{})
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	check := func(name, nodes, area, policy string) {
		t.Helper()
		m := cfg.FindMonitor(name)
		if got := strings.Join(m.Nodes, ","); got != nodes || m.Area != area || m.Policy != policy {
			t.Errorf("%s: nodes=%q area=%q policy=%q", name, got, m.Area, m.Policy)
		}
	}
	check("www", "", "", "default")
	check("bj-core", "bj,tj", "region:north", "bjops")
	check("bj-erp", "bj,tj", "region:north", "default") // its own policy wins
	check("bj-one", "bj", "region:north", "bjops")      // nodes= wins over the section
	check("sh-core", "sh", "sh", "default")
	check("dns", "", "", "default")
}

func TestScopeErrors(t *testing.T) {
	for src, code := range map[string]string{
		"$SCOPE region:west\nx IN PING 10.0.0.1\n":    "unknown_region",
		"$SCOPE nowhere\nx IN PING 10.0.0.1\n":        "unknown_node",
		"$SCOPE bj policy=none\nx IN PING 10.0.0.1\n": "unknown_policy",
		"$SCOPE * policy=bjops\n":                     "syntax",
		"x IN PING 10.0.0.1 nodes=region:nope\n":      "unknown_region",
	} {
		base := "@ IN SOA master. ops.example.com. ( 1 1m 15s 5s 3 )\nops IN CHANNEL log\nbjops IN POLICY chain=ops\nbj IN NODE region=north\n"
		_, _, diags := Load("t", []byte(base+src), Options{})
		found := false
		for _, d := range diags {
			if d.Code == code {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want %s, got %v", src, code, diags)
		}
	}
}

func TestUpsertRespectsSections(t *testing.T) {
	f, _ := Parse("t", []byte(scopeSrc))
	// A global monitor goes after the last global monitor, outside the
	// regional sections.
	if err := f.UpsertRecord(RecordSpec{Name: "api", Type: "HTTP", Target: "https://api.example.com/"}); err != nil {
		t.Fatal(err)
	}
	// A north monitor joins the north section.
	if err := f.UpsertRecord(RecordSpec{Name: "bj-new", Type: "PING", Target: "10.10.0.9", Area: "region:north"}); err != nil {
		t.Fatal(err)
	}
	// A section that does not exist yet is opened at the end.
	if err := f.UpsertRecord(RecordSpec{Name: "gz-core", Type: "PING", Target: "10.30.0.1", Area: "region:east"}); err != nil {
		t.Fatal(err)
	}
	_, cfg, diags := Load("t", f.Bytes(), Options{})
	if HasErrors(diags) {
		t.Fatalf("%v\n%s", diags, f.Bytes())
	}
	for name, area := range map[string]string{"api": "", "bj-new": "region:north", "gz-core": "region:east", "dns": "", "www": ""} {
		if m := cfg.FindMonitor(name); m == nil || m.Area != area {
			t.Errorf("%s: %+v\n%s", name, m, f.Bytes())
		}
	}
	// Editing keeps a record in its section unless asked to move it.
	f.UpsertRecord(RecordSpec{Name: "bj-new", Type: "PING", Target: "10.10.0.10"})
	f.UpsertRecord(RecordSpec{Name: "sh-core", Type: "PING", Target: "10.20.0.1", Area: "region:north", MoveArea: true})
	_, cfg, _ = Load("t", f.Bytes(), Options{})
	if cfg.FindMonitor("bj-new").Area != "region:north" || cfg.FindMonitor("sh-core").Area != "region:north" {
		t.Fatalf("moves:\n%s", f.Bytes())
	}
}

func TestParents(t *testing.T) {
	base := "@ IN SOA master. ops.example.com. ( 1 1m 15s 5s 3 )\n"
	_, cfg, diags := Load("t", []byte(base+"gw IN PING 10.0.0.1\nsw IN PING 10.0.0.2 parent=gw\nsrv IN TCP 10.0.0.3:22 parent=sw,gw\n"), Options{})
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	if got := strings.Join(cfg.FindMonitor("srv").Parents, ","); got != "sw,gw" {
		t.Fatal(got)
	}
	for src, code := range map[string]string{
		"a IN PING 10.0.0.1 parent=a\n":                                                           "invalid_value",
		"a IN PING 10.0.0.1 parent=nope\n":                                                        "unknown_parent",
		"a IN PING 10.0.0.1 parent=b\nb IN PING 10.0.0.2 parent=a\n":                              "parent_loop",
		"a IN PING 10.0.0.1 parent=c\nb IN PING 10.0.0.2 parent=a\nc IN PING 10.0.0.3 parent=b\n": "parent_loop",
	} {
		_, _, diags := Load("t", []byte(base+src), Options{})
		found := false
		for _, d := range diags {
			found = found || d.Code == code
		}
		if !found {
			t.Errorf("%q: want %s, got %v", src, code, diags)
		}
	}
}

func TestMaint(t *testing.T) {
	base := "@ IN SOA master. ops.example.com. ( 1 1m 15s 5s 3 )\nweb IN HTTP https://www.example.com/ tags=site:bj\ndb IN TCP 10.0.0.6:5432\n"
	_, cfg, diags := Load("t", []byte(base+`sun IN MAINT "机房维护" select="tag:site:bj" every=sun start=02:00 duration=2h notify=yes
up  IN MAINT select="cidr:10.0.0.0/8" at="2026-11-01 22:00" duration=3h
c   IN MAINT select=db cron="30 2 * * 1-5" duration=30m
`), Options{})
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	if len(cfg.Maints) != 3 || cfg.Maints[0].Title != "机房维护" || !cfg.Maints[0].Notify ||
		strings.Join(cfg.Maints[0].Monitors, ",") != "web" || strings.Join(cfg.Maints[1].Monitors, ",") != "db" {
		t.Fatalf("%+v", cfg.Maints)
	}
	for src, code := range map[string]string{
		"m IN MAINT select=web duration=1h\n":                                            "invalid_value",  // no schedule
		"m IN MAINT select=web every=sun duration=1h\n":                                  "missing_option", // no start
		"m IN MAINT select=web every=sun start=02:00\n":                                  "missing_option", // no duration
		"m IN MAINT select=web at=\"2026-11-01 22:00\" cron=\"* * * * *\" duration=1h\n": "invalid_value",
		"m IN MAINT select=status:critical at=\"2026-11-01 22:00\" duration=1h\n":        "invalid_value",
		"m IN MAINT select=web at=\"2026-11-01 22:00\" duration=30d\n":                   "invalid_value",
		"m IN MAINT select=nothing at=\"2026-11-01 22:00\" duration=1h\n":                "empty_select",
	} {
		_, _, diags := Load("t", []byte(base+src), Options{})
		found := false
		for _, d := range diags {
			found = found || d.Code == code
		}
		if !found {
			t.Errorf("%q: want %s, got %v", src, code, diags)
		}
	}
}

func TestRecordInjection(t *testing.T) {
	f, _ := Parse("t", []byte("@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n"))
	for name, r := range map[string]RecordSpec{
		"target":    {Name: "a", Type: "PING", Target: "10.0.0.1\n$SET notify.repeat=1s"},
		"option":    {Name: "a", Type: "HTTP", Target: "http://x/", Opts: []Opt{{Key: "title", Value: "x\rb IN NODE"}}},
		"key":       {Name: "a", Type: "HTTP", Target: "http://x/", Opts: []Opt{{Key: "k=v\nx", Value: "1"}}},
		"type":      {Name: "a", Type: "PING\nb IN PING 1.1.1.1", Target: "10.0.0.1"},
		"interval":  {Name: "a", Interval: "1m\nb", Type: "PING", Target: "10.0.0.1"},
		"comment":   {Name: "a", Type: "PING", Target: "10.0.0.1", Comment: "c\n$SET x=1"},
		"area":      {Name: "a", Type: "PING", Target: "10.0.0.1", Area: "x\n$SET y=1"},
		"separator": {Name: "a", Type: "PING", Target: "10.0.0.1", Opts: []Opt{{Key: "title", Value: "a b"}}},
	} {
		if err := f.UpsertRecord(r); err == nil {
			t.Errorf("%s: injection accepted:\n%s", name, f.Bytes())
		}
	}
	if err := f.SetTunables(map[string]string{"notify.repeat": "1m\n$SCOPE x"}, nil); err == nil {
		t.Error("tunable value with a line break accepted")
	}
	// Ordinary values with spaces, quotes and tabs still work.
	if err := f.UpsertRecord(RecordSpec{Name: "ok", Type: "HTTP", Target: "http://x/", Opts: []Opt{{Key: "title", Value: "a \"quoted\"\tvalue; with (parens)"}}}); err != nil {
		t.Fatal(err)
	}
	_, cfg, diags := Load("t", f.Bytes(), Options{})
	if HasErrors(diags) || cfg.FindMonitor("ok").Title != "a \"quoted\"\tvalue; with (parens)" {
		t.Fatalf("%v %q", diags, cfg.FindMonitor("ok").Title)
	}
}
