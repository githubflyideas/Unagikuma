package engine

import (
	"context"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// A PUSH monitor added long after start gets a full period before it is
// late, instead of being judged against the process start.
func TestNewPushMonitorGetsAFullPeriod(t *testing.T) {
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result {
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK}
		},
		Send:      func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink:      newMemSink(),
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{"engine.first-round-delay": "0s", "localnet.enabled": "off", "suspend.tick": "200ms"})
	if err != nil {
		t.Fatal(err)
	}
	e.Apply(Config{Tunables: ts, Location: time.UTC})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(700 * time.Millisecond) // longer than the push period below
	push := MonitorSpec{Name: "job", Title: "job", Push: true, PushPeriod: 600 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DefHash: "j",
		Interval: 600 * time.Millisecond, Probe: probe.Spec{Name: "job", Type: "PUSH"}}
	e.Apply(Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{push}})
	time.Sleep(300 * time.Millisecond)
	if r, _ := e.Row("job"); r.Status == StatusCritical {
		t.Fatal("a new PUSH monitor was failed before its first period ended")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if r, _ := e.Row("job"); r.Status == StatusCritical {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no push after a full period: should be late")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
