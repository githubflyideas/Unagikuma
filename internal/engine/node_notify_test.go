package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// newClusterEnvSink is newClusterEnv with a caller-provided sink.
func newClusterEnvSink(t *testing.T, cfg Config, sink *intentRecorder) *clusterEnv {
	t.Helper()
	fc := &fakeCluster{votes: map[string]probe.Vote{}, silent: map[string]bool{}, asked: map[string]int{}}
	e := New(Deps{
		Probe: func(ctx context.Context, s probe.Spec) probe.Result { return fc.result(cfg.Self) },
		Send: func(context.Context, notify.Channel, *notify.Message) notify.Attempt {
			return notify.Attempt{OK: true}
		},
		Sink:      sink,
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
		Remote:    fc,
	})
	fc.e = e
	ts, err := tunables.New(map[string]string{
		"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "cluster.vote-grace": "300ms", "cluster.startup-grace": "0s", "localnet.enabled": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tunables = ts
	e.Apply(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &clusterEnv{t: t, e: e, sink: sink.memSink, fc: fc}
}

// intentRecorder records every intent the engine persists.
type intentRecorder struct {
	*memSink
	mu      sync.Mutex
	intents []*Intent
}

func (s *intentRecorder) SaveIntent(in *Intent) error {
	s.mu.Lock()
	cp := *in
	s.intents = append(s.intents, &cp)
	s.mu.Unlock()
	return nil
}

func (s *intentRecorder) system(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	n := 0
	for _, in := range s.intents {
		// SaveIntent fires again on dispatch state updates; an intent is
		// one notice, counted once by its id.
		if in.System && in.Kind == kind && !seen[in.ID] {
			seen[in.ID] = true
			n++
		}
	}
	return n
}

// delivered reports whether an intent of the kind reached IntentDone.
func (s *intentRecorder) delivered(kind string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range s.intents {
		if in.System && in.Kind == kind && in.State == IntentDone {
			return true
		}
	}
	return false
}

// A node that stays away must produce a channel-bound SYSTEM problem
// intent through the durable pipeline, and its return a recovery intent —
// not only an event-stream entry. Reproduction for the node-down
// notification gap measured live on 2026-11-22.
func TestClusterNodeOfflineNotifies(t *testing.T) {
	cfg := clusterCfg(clusterMonitor("m", "a"), "a")
	cfg.Nodes[0].Timeout = 100 * time.Millisecond
	// A real installation routes the system policy to a channel; without
	// one the intent has an empty chain and can never be delivered.
	cfg.Channels = map[string]ChannelSpec{"wh": {Channel: notify.Channel{Name: "wh", Kind: "log"}}}
	cfg.Policies = map[string]PolicySpec{"default": {Name: "default", Chain: []string{"wh"}, Mode: DeliverAll,
		Notify: NotifyPolicy{Max: 3, Repeat: time.Hour, Recovery: true}}}
	sink := &intentRecorder{memSink: newMemSink()}
	c := newClusterEnvSink(t, cfg, sink)
	c.waitOpen(nodeOfflineKey("a"), true)
	deadline := time.Now().Add(3 * time.Second)
	for sink.system("PROBLEM") == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := sink.system("PROBLEM"); n != 1 {
		t.Fatalf("system PROBLEM intents = %d, want exactly 1", n)
	}
	// The notice names the node, this observing site and the last-contact
	// time (the honesty standard for system notices).
	sink.mu.Lock()
	var body string
	for _, in := range sink.intents {
		if in.System && in.Kind == "PROBLEM" {
			body = in.Message.Body
		}
	}
	sink.mu.Unlock()
	if !strings.Contains(body, "Last contact:") {
		t.Fatalf("problem notice body lacks the last-contact time: %q", body)
	}
	// The recovery notice follows actual delivery ("whoever was told
	// hears it is over"): wait until the problem intent is delivered
	// before the node returns, as in a real outage.
	deadline = time.Now().Add(3 * time.Second)
	for !sink.delivered("PROBLEM") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !sink.delivered("PROBLEM") {
		t.Fatal("system PROBLEM intent was never delivered")
	}
	c.online("a")
	c.waitOpen(nodeOfflineKey("a"), false)
	deadline = time.Now().Add(3 * time.Second)
	for sink.system("RECOVERY") == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := sink.system("RECOVERY"); n != 1 {
		t.Fatalf("system RECOVERY intents = %d, want exactly 1", n)
	}
}
