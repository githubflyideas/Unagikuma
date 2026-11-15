package engine

import (
	"context"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// A value beyond crit= fails the check with its own reason; beyond warn=
// it is degraded; a redefined monitor starts its samples over.
func TestValueRules(t *testing.T) {
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result { return probe.Result{Vote: probe.VoteOK} },
		Sink:  newMemSink(), HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "localnet.enabled": "off"})
	spec := MonitorSpec{Name: "room", Title: "room", DefHash: "a", Interval: time.Hour, Attempts: 1,
		Probe: probe.Spec{Name: "room", Type: "HTTP", Target: "http://127.0.0.1/t", Timeout: time.Second,
			Opts: map[string]string{"json-path": "$.temp", "warn": "35", "crit": "40"}}}
	e.Apply(Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{spec}})
	m := e.monitors["room"]
	read := func(v float64) probe.Result {
		return e.applyValues(m, probe.Result{Vote: probe.VoteOK, Detail: "HTTP 200", Metrics: map[string]float64{"value": v}}, time.Now())
	}
	if r := read(30); r.Vote != probe.VoteOK || r.Degraded || r.Metrics["value.current"] != 30 {
		t.Fatalf("30: %+v", r)
	}
	if r := read(37); r.Vote != probe.VoteOK || !r.Degraded {
		t.Fatalf("37: %+v", r)
	}
	if r := read(42); r.Vote != probe.VoteFail || r.Reason != probe.ReasonValueBreach || r.Detail != "42, limit >40" {
		t.Fatalf("42: %+v", r)
	}
	// Rules that get no value say so instead of never firing.
	if r := e.applyValues(m, probe.Result{Vote: probe.VoteOK, Detail: "HTTP 200"}, time.Now()); !r.Degraded || r.Detail != "the check reported no value for these rules" {
		t.Fatalf("no value: %+v", r)
	}
	// A failed check has no value to judge.
	if r := e.applyValues(m, probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout}, time.Now()); r.Reason != probe.ReasonTimeout {
		t.Fatalf("failure: %+v", r)
	}
	// Without rules nothing changes.
	plain := MonitorSpec{Name: "p", DefHash: "p", Probe: probe.Spec{Type: "HTTP", Opts: map[string]string{"json-path": "$.x"}}}
	if r := e.applyValues(&plain, probe.Result{Vote: probe.VoteOK, Metrics: map[string]float64{"value": 1e9}}, time.Now()); r.Degraded || r.Vote != probe.VoteOK {
		t.Fatalf("no rules: %+v", r)
	}
	before := e.values["room"]
	spec.DefHash = "b"
	e.Apply(Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{spec}})
	m = e.monitors["room"]
	read(30)
	if e.values["room"] == before {
		t.Fatal("samples kept across a redefinition")
	}
	e.Apply(Config{Tunables: ts, Location: time.UTC})
	if _, ok := e.values["room"]; ok {
		t.Fatal("samples of a removed monitor kept")
	}
	// Values kept before a restart are the tracker's first samples.
	e.LoadValues(map[string][]valuerule.Sample{"room": {{At: time.Now().Add(-time.Hour), V: 33}}})
	spec.DefHash = "c"
	e.Apply(Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{spec}})
	e.applyValues(e.monitors["room"], probe.Result{Vote: probe.VoteOK, Metrics: map[string]float64{"value": 34}}, time.Now())
	if s := e.values["room"].tr.Samples(); len(s) != 2 || s[0].V != 33 {
		t.Fatalf("restored samples: %v", s)
	}
}

// A condition of two fields fails the check when it holds, degrades it for
// warn=, and a counter's comparison is on its rate.
func TestConditionRules(t *testing.T) {
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result { return probe.Result{Vote: probe.VoteOK} },
		Sink:  newMemSink(), HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "localnet.enabled": "off"})
	spec := MonitorSpec{Name: "web", Title: "web", DefHash: "a", Interval: time.Hour, Attempts: 1,
		Probe: probe.Spec{Name: "web", Type: "HTTP", Target: "http://127.0.0.1/s", Timeout: time.Second,
			Opts: map[string]string{"app": "nginx", "crit": "active>800 and waiting>500", "warn": "active>400 or requests>100"}}}
	e.Apply(Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{spec}})
	m := e.monitors["web"]
	at := time.Now()
	read := func(d time.Duration, active, waiting, requests float64) probe.Result {
		return e.applyValues(m, probe.Result{Vote: probe.VoteOK, Detail: "nginx", Metrics: map[string]float64{
			"value": active, "app.active": active, "app.waiting": waiting, "app.requests": requests}}, at.Add(d))
	}
	if r := read(0, 100, 10, 1000); r.Vote != probe.VoteOK || r.Degraded {
		t.Fatalf("quiet: %+v", r)
	}
	if r := read(time.Minute, 900, 100, 1000); r.Vote != probe.VoteOK || !r.Degraded || r.Detail != "active 900 >400" {
		t.Fatalf("busy only: %+v", r)
	}
	if r := read(2*time.Minute, 900, 600, 1000); r.Vote != probe.VoteFail || r.Reason != probe.ReasonValueBreach || r.Detail != "active 900 >800 and waiting 600 >500" {
		t.Fatalf("both: %+v", r)
	}
	// 12000 requests in a minute is 200/s, over warn's 100/s on its own.
	if r := read(3*time.Minute, 100, 10, 1000+12000); r.Vote != probe.VoteOK || !r.Degraded || r.Detail != "requests 200/s >100/s" {
		t.Fatalf("rate: %+v", r)
	}
}
