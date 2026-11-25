package engine

import (
	"context"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// A paused monitor's open problem sends no repeats and no escalations:
// nothing new is known about it while it is not checked.
func TestPausedMonitorSendsNoRepeats(t *testing.T) {
	for _, paused := range []bool{false, true} {
		e := New(Deps{
			Probe: func(context.Context, probe.Spec) probe.Result {
				return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout, Evidence: &probe.Evidence{}}
			},
			Sink: newMemSink(), HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
			Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
		})
		ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "localnet.enabled": "off"})
		pol := PolicySpec{Name: "p", Chain: []string{"ops"}, Mode: DeliverFallback, Notify: NotifyPolicy{Max: 3, Repeat: time.Minute},
			Escalate: []EscalationStep{{After: time.Minute, Channels: []string{"boss"}}}}
		e.Apply(Config{Tunables: ts, Location: time.UTC, Policies: map[string]PolicySpec{"p": pol},
			Monitors: []MonitorSpec{{Name: "u", Title: "u", Policy: "p", Paused: paused,
				Probe: probe.Spec{Name: "u", Type: "TCP", Target: "10.0.0.1:80", Timeout: 50 * time.Millisecond}, Interval: time.Hour, Attempts: 1, DefHash: "u"}}})
		long := time.Now().Add(-time.Hour)
		inc := &Incident{ID: 1, Monitor: "u", Policy: "p", Kind: IncidentNormal}
		inc.State = IncidentState{OpenedAt: long, HardAt: long, NotifyCount: 1, LastNotifyAt: long, Tracked: true}
		e.incidents[1] = inc
		now := time.Now()
		e.advanceIncidents(now)
		e.escalate(now)
		sent := 0
		for _, in := range e.intents {
			if in.IncidentID == 1 {
				sent++
			}
		}
		if paused && sent != 0 {
			t.Fatalf("paused: %d notices queued", sent)
		}
		if !paused && sent == 0 {
			t.Fatal("not paused: the repeat and the escalation are due")
		}
	}
}

// A monitor edited, then paused and resumed before its first round, has no
// result under its definition: it is due at once, not a whole interval
// after a check from before the edit. A resumed monitor is checked at once
// too, whatever its last result.
func TestResumeSchedulesAtOnce(t *testing.T) {
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result { return probe.Result{Vote: probe.VoteOK} },
		Sink:  newMemSink(), HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "2s", "suspend.recheck-spread": "0s", "localnet.enabled": "off"})
	spec := func(hash string, paused bool) Config {
		return Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{{Name: "w", Title: "w", Paused: paused,
			Probe: probe.Spec{Name: "w", Type: "HTTP", Target: "http://10.0.0.1/", Timeout: time.Second}, Interval: 5 * time.Minute, Attempts: 1, DefHash: hash}}}
	}
	e.Apply(spec("a", false))
	rt := e.states["w"]
	rt.State = MonState{Avail: StatusOK, Hard: true}
	rt.LastCheck = time.Now().Add(-time.Minute) // a check from before the edit
	e.Apply(spec("b", false))                   // edited: PENDING until its first round
	if rt.State.Avail != StatusPending {
		t.Fatalf("after the edit: %s", rt.State.Avail)
	}
	e.Apply(spec("b", true))  // paused
	e.Apply(spec("b", false)) // resumed
	due, ok := e.sched.Due("w")
	if !ok || due.After(time.Now().Add(3*time.Second)) {
		t.Fatalf("pending and resumed: due in %s (the interval is 5m)", time.Until(due).Round(time.Second))
	}
	// A resumed monitor with a result is checked at once as well.
	rt.State = MonState{Avail: StatusOK, Hard: true}
	rt.LastCheck = time.Now().Add(-10 * time.Second)
	e.Apply(spec("b", true))
	e.Apply(spec("b", false))
	if due, _ := e.sched.Due("w"); due.After(time.Now().Add(3 * time.Second)) {
		t.Fatalf("resumed with a result: due in %s", time.Until(due).Round(time.Second))
	}
	// An ordinary re-apply keeps the schedule.
	rt.LastCheck = time.Now()
	e.sched.Set("w", time.Now().Add(4*time.Minute))
	e.Apply(spec("b", false))
	if due, _ := e.sched.Due("w"); due.Before(time.Now().Add(3 * time.Minute)) {
		t.Fatalf("a plain re-apply moved the due time to %s", time.Until(due).Round(time.Second))
	}
}
