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
