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
