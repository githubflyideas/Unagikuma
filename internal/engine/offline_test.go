package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// intentSink counts the monitor notifications the engine queues.
type intentSink struct {
	*memSink
	mu       sync.Mutex
	problems int
}

func (s *intentSink) SaveIntent(in *Intent) error {
	s.mu.Lock()
	if !in.System && in.Kind == "PROBLEM" {
		s.problems++
	}
	s.mu.Unlock()
	return nil
}

// A host started without network: every name fails to resolve because no
// resolver answers. That is one local outage, not ten broken sites: no
// monitor notifies and they show UNKNOWN, not CRITICAL.
func TestColdStartOffline(t *testing.T) {
	sink := &intentSink{memSink: newMemSink()}
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonDNSResolve, Detail: "lookup " + s.Target + ": no resolver reachable"}
		},
		Send:      func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink:      sink,
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.window": "10s"})
	if err != nil {
		t.Fatal(err)
	}
	var mons []MonitorSpec
	for i := range 10 {
		n := fmt.Sprintf("site%d", i)
		mons = append(mons, MonitorSpec{Name: n, Title: n, Interval: 100 * time.Millisecond, Attempts: 2, RecoverAttempts: 1, DefHash: n,
			Probe: probe.Spec{Name: n, Type: "HTTP", Target: fmt.Sprintf("https://www.example%d.com/", i), Timeout: 50 * time.Millisecond}})
	}
	e.Apply(Config{Tunables: ts, Monitors: mons, LocalDetect: true, Location: time.UTC})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(5 * time.Second)
	for {
		unknown := 0
		for _, r := range e.Rows() {
			if r.Status == StatusUnknown {
				unknown++
			}
		}
		if unknown == len(mons) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not all UNKNOWN: %+v", e.Rows())
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second) // more rounds, still failing
	for _, r := range e.Rows() {
		if r.Status == StatusCritical {
			t.Errorf("%s is CRITICAL during the local outage", r.Name)
		}
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.problems != 0 {
		t.Fatalf("%d monitor notifications for one local outage", sink.problems)
	}
	if h := e.Overview().Health; h.Uplink == "ok" {
		t.Fatalf("the uplink outage was not recognised: %+v", h)
	}
}

// With the network working (another target answers), a dead site is not
// held: it alerts as usual.
func TestDeadSiteOnWorkingNetworkAlerts(t *testing.T) {
	sink := &intentSink{memSink: newMemSink()}
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			if s.Name == "dead" {
				return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout, Evidence: &probe.Evidence{Peer: "203.0.113.9:443"}}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Evidence: &probe.Evidence{Peer: "198.51.100.7:443"}}
		},
		Send:      func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink:      sink,
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.window": "10s"})
	mon := func(n, target string) MonitorSpec {
		return MonitorSpec{Name: n, Title: n, Interval: 100 * time.Millisecond, Attempts: 2, RecoverAttempts: 1, DefHash: n,
			Probe: probe.Spec{Name: n, Type: "HTTP", Target: target, Timeout: 50 * time.Millisecond}}
	}
	e.Apply(Config{Tunables: ts, LocalDetect: true, Location: time.UTC,
		Monitors: []MonitorSpec{mon("dead", "https://203.0.113.9/"), mon("ok", "https://198.51.100.7/")}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r, _ := e.Row("dead"); r.Status == StatusCritical {
			return
		}
		if time.Now().After(deadline) {
			r, _ := e.Row("dead")
			t.Fatalf("a dead site on a working network did not alert: %+v", r)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
