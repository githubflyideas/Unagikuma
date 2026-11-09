package engine

import (
	"context"
	"strings"
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

// Changing the check of a monitor that is down keeps its incident: no
// second PROBLEM for the same outage, and a RECOVERY once the new check
// passes.
func TestRedefinitionKeepsOpenIncident(t *testing.T) {
	var up atomic.Bool
	var mu sync.Mutex
	var sent []string
	sink := newMemSink()
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result {
			if up.Load() {
				return probe.Result{Vote: probe.VoteOK, Evidence: &probe.Evidence{}}
			}
			return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Evidence: &probe.Evidence{}}
		},
		Send: func(_ context.Context, ch notify.Channel, m *notify.Message) notify.Attempt {
			mu.Lock()
			sent = append(sent, m.Kind)
			mu.Unlock()
			return notify.Attempt{OK: true}
		},
		Sink: sink, HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{
		"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.enabled": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := func(target, hash string) Config {
		return Config{Tunables: ts, Location: time.UTC,
			Channels: map[string]ChannelSpec{"ops": {Channel: notify.Channel{Name: "ops", Kind: "log"}}},
			Policies: map[string]PolicySpec{"p": {Name: "p", Chain: []string{"ops"}, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}},
			Monitors: []MonitorSpec{{Name: "web", Title: "web", Policy: "p",
				Probe:    probe.Spec{Name: "web", Type: "TCP", Target: target, Timeout: 100 * time.Millisecond},
				Interval: 50 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DefHash: hash}},
		}
	}
	e.Apply(cfg("10.0.0.1:80", "a"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(sent, " ")
	}
	waitFor := func(what string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(got(), what); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("no %s: %q", what, got())
			}
		}
	}
	waitFor("PROBLEM")
	incidents := func() int {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.incidents)
	}
	before := incidents()
	e.Apply(cfg("10.0.0.1:8080", "b"))
	time.Sleep(600 * time.Millisecond) // many rounds of the new, still failing check
	if n := strings.Count(got(), "PROBLEM"); n != 1 || incidents() != before {
		t.Fatalf("after redefinition: sent %q, incidents %d (was %d)", got(), incidents(), before)
	}
	up.Store(true)
	waitFor("RECOVERY")
}
