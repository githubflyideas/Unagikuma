package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

type kindSink struct {
	*memSink
	mu    sync.Mutex
	kinds []string
}

func (s *kindSink) SaveIntent(in *Intent) error {
	s.mu.Lock()
	s.kinds = append(s.kinds, in.Kind)
	s.mu.Unlock()
	return nil
}

func (s *kindSink) count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range s.kinds {
		if k == kind {
			n++
		}
	}
	return n
}

// Slow, then down: the degraded incident is replaced by the failure
// incident without a "recovered" message in between. Paused shows PAUSED.
func TestSlowThenDownAndPaused(t *testing.T) {
	var down atomic.Bool
	sink := &kindSink{memSink: newMemSink()}
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result {
			if down.Load() {
				return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Evidence: &probe.Evidence{Peer: "10.0.0.5:80"}}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Latency: 200 * time.Millisecond, Evidence: &probe.Evidence{Peer: "10.0.0.5:80"}}
		},
		Send:      func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink:      sink,
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.tick": "200ms", "engine.round-grace": "0s", "localnet.enabled": "off"})
	np := DefaultNotifyPolicy(ts)
	np.NotifyDegraded = true
	mon := MonitorSpec{Name: "web", Title: "web", Interval: 50 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DegradedAttempts: 1,
		SlowAfter: 50 * time.Millisecond, DefHash: "w", Probe: probe.Spec{Name: "web", Type: "TCP", Target: "10.0.0.5:80", Timeout: time.Second}}
	cfg := Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{mon},
		Policies: map[string]PolicySpec{"default": {Name: "default", Mode: DeliverFallback, Notify: np}}}
	e.Apply(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	wait := func(what string, f func() bool) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for !f() {
			if time.Now().After(end) {
				t.Fatalf("timed out: %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	status := func() string { r, _ := e.Row("web"); return r.Status }
	wait("degraded", func() bool { return status() == StatusDegraded })
	down.Store(true)
	wait("critical", func() bool { return status() == StatusCritical && sink.count("PROBLEM") >= 1 })
	time.Sleep(200 * time.Millisecond)
	if n := sink.count("RECOVERY"); n != 0 {
		t.Fatalf("%d recovery messages while the monitor went from slow to down", n)
	}

	mon.Paused = true
	cfg.Monitors = []MonitorSpec{mon}
	e.Apply(cfg)
	wait("paused", func() bool { return status() == StatusPaused })
}
