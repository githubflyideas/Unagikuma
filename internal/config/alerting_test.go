package config

import (
	"strings"
	"testing"
	"time"
)

const soa = "@ IN SOA master. me.example.com. ( 2026102101 1m 15s 5s 3 )\n"

func TestPeriodsGroupsEscalation(t *testing.T) {
	src := soa + `office IN PERIOD "mon-fri 09:00-18:00"
night  IN PERIOD "mon-sun 22:00-07:00"
im     IN CHANNEL log period=office
sms    IN CHANNEL log period=!office
ops    IN CHANNEL group members=im,sms
boss   IN CHANNEL log
pager  IN POLICY chain=ops escalate=30m:boss,2h:boss+sms
web    IN TCP 10.0.0.1:80 policy=pager handler="/bin/true $SERVICESTATE$"
`
	_, cfg, diags := load(t, src)
	if errs := errorsOf(diags); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(cfg.Periods) != 2 || cfg.FindPeriod("office") == nil {
		t.Fatalf("periods: %+v", cfg.Periods)
	}
	if c := cfg.FindChannel("sms"); c.Period != "!office" {
		t.Fatalf("period: %+v", c)
	}
	if c := cfg.FindChannel("ops"); strings.Join(c.Members, ",") != "im,sms" {
		t.Fatalf("members: %+v", c)
	}
	p := cfg.FindPolicy("pager")
	if len(p.Escalate) != 2 || p.Escalate[1].After != 2*time.Hour || EscalateText(p.Escalate) != "30m:boss,2h:boss+sms" {
		t.Fatalf("escalate: %+v", p.Escalate)
	}
	if m := cfg.FindMonitor("web"); m.Opts["handler"] == "" {
		t.Fatalf("handler: %+v", m.Opts)
	}

	_, _, ds := load(t, soa+"a IN CHANNEL log\nb IN CHANNEL log\noncall IN CHANNEL group members=a,b rotate=weekly handoff=\"mon 10:00\" start=2026-11-02\n")
	if errs := errorsOf(ds); len(errs) > 0 {
		t.Errorf("rotation: %v", errs)
	}
	bad := map[string]string{
		"x IN PERIOD \"someday 09:00-10:00\"\n": "someday",
		"a IN CHANNEL log period=nope\n":        "nope",
		"a IN CHANNEL log members=b\n":          "members",
		"g IN CHANNEL group\n":                  "member",
		"g IN CHANNEL group members=zz\n":       "zz",
		"a IN CHANNEL log\ng IN CHANNEL group members=a\nh IN CHANNEL group members=g\n": "group",
		"a IN CHANNEL log\np IN POLICY chain=a escalate=2h:a,30m:a\n":                    "increase",
		"a IN CHANNEL log\np IN POLICY chain=a escalate=30m:zz\n":                        "zz",
		"a IN CHANNEL log\np IN POLICY chain=a escalate=soon\n":                          "DELAY",
		"a IN CHANNEL log\np IN POLICY chain=a network=zz\n":                             "network channel",
		"a IN CHANNEL log\ng IN CHANNEL group members=a rotate=monthly\n":                "rotate must be",
		"a IN CHANNEL log\ng IN CHANNEL group members=a handoff=09:00\n":                 "needs rotate=",
		"a IN CHANNEL log\ng IN CHANNEL group members=a rotate=daily x=1\n":              "option x",
	}
	for s, want := range bad {
		_, _, ds := load(t, soa+s)
		errs := errorsOf(ds)
		if len(errs) == 0 {
			t.Errorf("%q: no error", s)
			continue
		}
		var msgs []string
		for _, e := range errs {
			msgs = append(msgs, e.Msg)
		}
		if !strings.Contains(strings.Join(msgs, "|"), want) {
			t.Errorf("%q: %v, want %q", s, msgs, want)
		}
	}
}

func TestStatusPageDomainAndSubscribe(t *testing.T) {
	ok := soa + "mail IN CHANNEL email host=localhost from=a@b.c to=d@e.f\np IN STATUSPAGE \"S\" domain=Status.Example.com. subscribe=mail\n"
	_, cfg, diags := load(t, ok)
	if errs := errorsOf(diags); len(errs) > 0 || cfg.StatusPages[0].Domain != "status.example.com" {
		t.Fatalf("%v %+v", errs, cfg.StatusPages)
	}
	for src, want := range map[string]string{
		"p IN STATUSPAGE \"S\" domain=localhost\n":                             "host name",
		"p IN STATUSPAGE \"S\" domain=a.b\nq IN STATUSPAGE \"T\" domain=a.b\n": "already used",
		"l IN CHANNEL log\np IN STATUSPAGE \"S\" subscribe=l\n":                "email channel",
		"p IN STATUSPAGE \"S\" subscribe=nope\n":                               "email channel",
	} {
		_, _, ds := load(t, soa+src)
		errs := errorsOf(ds)
		if len(errs) == 0 || !strings.Contains(errs[0].Msg, want) {
			t.Errorf("%q: %v, want %q", src, errs, want)
		}
	}
}
