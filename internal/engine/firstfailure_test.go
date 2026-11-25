package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/observation"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// slowObsSink answers observation reads only after a long while.
type slowObsSink struct {
	*memSink
	wait time.Duration
}

func (s *slowObsSink) RecordSiteObservation(observation.Row) error { return nil }
func (s *slowObsSink) SiteObservationSnapshot(string, string, []string, time.Time) (*observation.Snapshot, error) {
	time.Sleep(s.wait)
	return &observation.Snapshot{}, nil
}

// A monitor that other sites check too tells them the moment it first
// fails — once per failure, not on every failing check — and a monitor
// only this site checks does not.
func TestFirstFailureAsksOtherSites(t *testing.T) {
	var mu sync.Mutex
	down := map[string]bool{}
	calls := map[string]int{}
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			mu.Lock()
			defer mu.Unlock()
			if down[s.Name] {
				return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Evidence: &probe.Evidence{}}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Evidence: &probe.Evidence{}}
		},
		Send: func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink: newMemSink(), HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
		FirstFailure: func(m, _ string, sites []string) {
			mu.Lock()
			calls[m]++
			mu.Unlock()
		},
	})
	ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "engine.round-grace": "0s", "localnet.enabled": "off"})
	mon := func(name string) MonitorSpec {
		return MonitorSpec{Name: name, Title: name, Probe: probe.Spec{Name: name, Type: "TCP", Target: "10.0.0.1:80", Timeout: 100 * time.Millisecond},
			Interval: 40 * time.Millisecond, Retry: 40 * time.Millisecond, Attempts: 3, RecoverAttempts: 1, DefHash: name}
	}
	e.Apply(Config{Tunables: ts, Monitors: []MonitorSpec{mon("global"), mon("solo")}, Location: time.UTC,
		ObservationSites: map[string][]string{"global": {"a", "b", "c"}, "solo": {"a"}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	status := func(n string) string { r, _ := e.Row(n); return r.Status }
	wait := func(what string, f func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); !f(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("timed out: %s", what)
			}
		}
	}
	count := func(n string) int { mu.Lock(); defer mu.Unlock(); return calls[n] }
	wait("up", func() bool { return status("global") == "ok" && status("solo") == "ok" })
	mu.Lock()
	down["global"], down["solo"] = true, true
	mu.Unlock()
	wait("confirmed", func() bool { return status("global") == "critical" && status("solo") == "critical" })
	time.Sleep(200 * time.Millisecond) // more failing checks
	if count("global") != 1 || count("solo") != 0 {
		t.Fatalf("calls %v, want global once and solo never", calls)
	}
	// Recovered, then failing again: asked again.
	mu.Lock()
	down["global"] = false
	mu.Unlock()
	wait("recovered", func() bool { return status("global") == "ok" })
	mu.Lock()
	down["global"] = true
	mu.Unlock()
	wait("second failure", func() bool { return count("global") == 2 })
}

// Preparing an alert reads nothing in the engine loop; where the alert is
// sent, a slow store holds back that one alert for observationReadWait at
// most, and then it goes without the site line. Every channel of the
// alert shares the one read.
func TestObservationReadIsBounded(t *testing.T) {
	e := New(Deps{Sink: &slowObsSink{memSink: newMemSink(), wait: 2 * time.Second}})
	ts, _ := tunables.New(nil)
	e.cfg = Config{Tunables: ts, Location: time.UTC, Nodes: []NodeSpec{{Name: "a"}, {Name: "b"}},
		ObservationSites: map[string][]string{"web": {"a", "b"}}}
	e.monitors["web"] = &MonitorSpec{Name: "web", DefHash: "h"}
	e.states["web"] = &monRuntime{}
	start := time.Now()
	f := e.observationFetch("web")
	if f == nil || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("preparing took %v", time.Since(start))
	}
	if observationReadWait < 3*time.Second {
		t.Fatalf("read wait %v, want at least 3s", observationReadWait)
	}
	f.wait = 100 * time.Millisecond
	start = time.Now()
	if snap := f.get(); snap != nil {
		t.Fatal("a late snapshot was used")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("waited %v", d)
	}
	start = time.Now()
	f.get() // the second channel does not wait again
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("read twice: %v", d)
	}
}
