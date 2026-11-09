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
	"github.com/githubflyideas/unagikuma/internal/oncall"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// A rotation notifies whoever is on call (a stand-in first), and the
// others in turn only when that fails.
func TestRotationDelivery(t *testing.T) {
	var sent []string
	fail := map[string]bool{}
	e := New(Deps{Send: func(_ context.Context, ch notify.Channel, _ *notify.Message) notify.Attempt {
		sent = append(sent, ch.Name)
		if fail[ch.Name] {
			return notify.Attempt{Error: "unreachable"}
		}
		return notify.Attempt{OK: true}
	}})
	rot, err := oncall.Parse(map[string]string{"rotate": "daily", "handoff": "00:00", "start": "2026-11-01"}, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	log := func(name string) ChannelSpec { return ChannelSpec{Channel: notify.Channel{Name: name, Kind: "log"}} }
	ops := log("ops")
	ops.Kind, ops.Members, ops.Rotation = "group", []string{"a", "b", "c"}, rot
	e.cfg = Config{Location: time.UTC, Channels: map[string]ChannelSpec{"a": log("a"), "b": log("b"), "c": log("c"), "ops": ops}}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) // b's day
	send := func() (notify.Attempt, string) {
		sent = nil
		att, _ := e.sendRotation(context.Background(), e.groupMembers(ops, now), &notify.Message{Kind: "PROBLEM"})
		return att, strings.Join(sent, ",")
	}
	if att, got := send(); !att.OK || got != "b" {
		t.Fatalf("on call: %v %q", att, got)
	}
	fail["b"] = true
	if att, got := send(); !att.OK || got != "b,c" {
		t.Fatalf("fallback: %v %q", att, got)
	}
	e.cfg.Overrides = []oncall.Override{{ID: 1, Rotation: "ops", Member: "a", From: now.Add(-time.Hour), Until: now.Add(time.Hour)}}
	if att, got := send(); !att.OK || got != "a" {
		t.Fatalf("stand-in: %v %q", att, got)
	}
	fail["a"], fail["c"] = true, true
	if att, got := send(); att.OK || got != "a,b,c" || !strings.Contains(att.Error, "c: unreachable") {
		t.Fatalf("all fail: %v %q", att, got)
	}
}

// The recovery of an incident goes to the member who was told about the
// problem, even when the shift has changed hands since (here: the stand-in
// who took the problem has ended), not to whoever is on call now.
func TestRecoveryGoesToResponder(t *testing.T) {
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
			sent = append(sent, m.Kind+":"+ch.Name)
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
	rot, err := oncall.Parse(map[string]string{"rotate": "daily", "handoff": "00:00", "start": "2026-01-01"}, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	scheduled := rot.At(now, time.UTC).Member
	standIn := map[string]string{"a": "b", "b": "a"}[scheduled]
	log := func(name string) ChannelSpec { return ChannelSpec{Channel: notify.Channel{Name: name, Kind: "log"}} }
	ops := log("ops")
	ops.Kind, ops.Members, ops.Rotation = "group", []string{"a", "b"}, rot
	cfg := func(overrides []oncall.Override) Config {
		return Config{Tunables: ts, Location: time.UTC, Overrides: overrides,
			Channels: map[string]ChannelSpec{"a": log("a"), "b": log("b"), "ops": ops},
			Policies: map[string]PolicySpec{"p": {Name: "p", Chain: []string{"ops"}, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}},
			Monitors: []MonitorSpec{{Name: "web", Title: "web", Policy: "p",
				Probe:    probe.Spec{Name: "web", Type: "TCP", Target: "10.0.0.1:80", Timeout: 100 * time.Millisecond},
				Interval: 50 * time.Millisecond, Attempts: 1, RecoverAttempts: 1}},
		}
	}
	e.Apply(cfg([]oncall.Override{{ID: 1, Rotation: "ops", Member: standIn, From: now.Add(-time.Hour), Until: now.Add(time.Hour)}}))
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
	waitFor("PROBLEM:" + standIn)
	// The stand-in is cancelled: the scheduled member is on call again.
	e.Apply(cfg(nil))
	if o := e.OnCallOrder("ops", time.Now()); len(o) == 0 || o[0] != scheduled {
		t.Fatalf("order after the stand-in: %v", o)
	}
	up.Store(true)
	waitFor("RECOVERY")
	if !strings.Contains(got(), "RECOVERY:"+standIn) || strings.Contains(got(), "RECOVERY:"+scheduled) {
		t.Fatalf("recovery went to the wrong member: %q", got())
	}
}
