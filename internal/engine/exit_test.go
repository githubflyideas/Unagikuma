package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// The internet exit is pinged and shown, but it is not an anchor: an exit
// that stops answering never counts as evidence of a local outage. It is
// only pinged while an internet target fails.
func TestInternetExit(t *testing.T) {
	var exitDown atomic.Bool
	var pinged atomic.Int32
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			if s.Name == "anchor" && s.Target == "8.8.8.8" {
				pinged.Add(1)
				if exitDown.Load() {
					return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout}
				}
				return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Latency: 30 * time.Millisecond}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK}
		},
		Send:      func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink:      newMemSink(),
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s"})
	e.Apply(Config{Tunables: ts, Location: time.UTC, LocalDetect: true, Policies: map[string]PolicySpec{"default": {Name: "default", Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	// Run checks the anchors once at start, on its own goroutine: let that
	// finish first, or its result could land after the checks below.
	for deadline := time.Now().Add(5 * time.Second); e.Health().Exit == ""; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first anchor check did not finish")
		}
	}

	// Every internet target answers: the exit is not asked.
	e.checkAnchors(ctx)
	if h := e.Health(); h.Exit != "8.8.8.8" || !h.ExitOK || !h.ExitIdle || pinged.Load() != 0 {
		t.Fatalf("exit while the targets answer: %+v, pinged %d", h, pinged.Load())
	}
	// An internet target fails: now the exit is worth asking.
	e.do(func() {
		e.agentRT["site"] = &monRuntime{Scope: netscope.WAN, LastResult: probe.Result{Vote: probe.VoteFail}}
	})
	e.checkAnchors(ctx)
	h := e.Health()
	if h.Exit != "8.8.8.8" || !h.ExitOK || h.ExitIdle || h.ExitRTT != 30*time.Millisecond || h.ExitAt.IsZero() {
		t.Fatalf("exit after a reply: %+v", h)
	}
	exitDown.Store(true)
	e.checkAnchors(ctx)
	h = e.Health()
	if h.Exit != "8.8.8.8" || h.ExitOK {
		t.Fatalf("exit after a timeout: %+v", h)
	}
	if h.WANAnchors != 0 || h.Uplink == "wan_down" {
		t.Fatalf("the exit was used as an anchor: %+v", h)
	}
	if pinged.Load() < 2 {
		t.Fatalf("exit pinged %d times", pinged.Load())
	}

	// Turning it off clears it.
	off, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "localnet.exit-target": ""})
	e.Apply(Config{Tunables: off, Location: time.UTC, LocalDetect: true, Policies: map[string]PolicySpec{"default": {Name: "default", Mode: DeliverFallback, Notify: DefaultNotifyPolicy(off)}}})
	e.checkAnchors(ctx)
	if h := e.Health(); h.Exit != "" {
		t.Fatalf("exit still shown when off: %+v", h)
	}
}
