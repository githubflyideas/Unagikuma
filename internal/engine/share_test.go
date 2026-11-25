package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// share=yes takes the result of an identical check of another monitor
// that started within the window, waiting for it if it still runs; a
// different check, the monitor's own earlier check, or an old one is not
// shared, and monitors without share=yes always check for themselves.
func TestSharedProbe(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	run := func(_ context.Context, s probe.Spec) probe.Result {
		runs.Add(1)
		if s.Target == "slow" {
			<-release
		}
		return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Detail: s.Name, Evidence: &probe.Evidence{Peer: "x"},
			Metrics: map[string]float64{"latency_ms": 1}}
	}
	spec := func(name, target string, share bool) probe.Spec {
		return probe.Spec{Name: name, Type: "PING", Target: target, Timeout: time.Second, Opts: map[string]string{"count": "3"}, Share: share}
	}
	var sh sharing
	ctx := context.Background()
	now := time.Now()
	win := 10 * time.Second

	a := sh.probe(ctx, run, spec("a", "10.0.0.5", false), false, win, now)
	b := sh.probe(ctx, run, spec("b", "10.0.0.5", true), true, win, now.Add(time.Second))
	if runs.Load() != 1 || b.Detail != "a" {
		t.Fatalf("b did not share a's check: runs %d, %+v", runs.Load(), b)
	}
	b.Evidence.Peer, b.Metrics["latency_ms"] = "changed", 99
	if a.Evidence.Peer != "x" || a.Metrics["latency_ms"] != 1 {
		t.Fatal("a shared result is not a copy")
	}
	for _, c := range []struct {
		name string
		spec probe.Spec
		at   time.Duration
	}{
		{"own earlier check", spec("a", "10.0.0.5", true), 2 * time.Second},
		{"other options", probe.Spec{Name: "c", Type: "PING", Target: "10.0.0.5", Timeout: time.Second, Opts: map[string]string{"count": "5"}, Share: true}, 2 * time.Second},
		{"other target", spec("d", "10.0.0.6", true), 2 * time.Second},
		{"without share", spec("e", "10.0.0.5", false), 2 * time.Second},
		{"too old", spec("f", "10.0.0.5", true), 30 * time.Second},
	} {
		before := runs.Load()
		sh.probe(ctx, run, c.spec, c.spec.Share, win, now.Add(c.at))
		if runs.Load() != before+1 {
			t.Errorf("%s: shared", c.name)
		}
	}
	// A check still running is waited for, not repeated.
	var wg sync.WaitGroup
	var lead, follow probe.Result
	wg.Add(1)
	go func() { defer wg.Done(); lead = sh.probe(ctx, run, spec("g", "slow", false), false, win, now) }()
	for runs.Load() < 7 {
		time.Sleep(time.Millisecond)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		follow = sh.probe(ctx, run, spec("h", "slow", true), true, win, now.Add(time.Second))
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if runs.Load() != 7 || follow.Detail != "g" || lead.Detail != "g" {
		t.Fatalf("waiting follower: runs %d, %+v", runs.Load(), follow)
	}
	// Sharing off.
	before := runs.Load()
	sh.probe(ctx, run, spec("i", "10.0.0.5", true), true, 0, now)
	if runs.Load() != before+1 {
		t.Error("window 0 shared")
	}
}
