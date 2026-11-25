package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// Several monitors can check exactly the same thing: the same target, the
// same options, from the same nodes — typically one service watched by
// two teams. A monitor with share=yes takes the result of an identical
// check that started on this node within probe.share-window instead of
// sending its own: the target sees one check, and both monitors keep
// their own thresholds, incidents and notifications.

// runProbe runs a monitor's probe on this node, sharing a recent identical
// execution when the spec allows it. Safe for any goroutine.
func (e *Engine) runProbe(ctx context.Context, spec probe.Spec) probe.Result {
	return e.share.probe(ctx, e.deps.Probe, spec, spec.Share, time.Duration(e.shareWindow.Load()), e.now())
}

// flight is one probe execution others may share.
type flight struct {
	name  string // the monitor it ran for: never shared with itself
	start time.Time
	done  chan struct{}
	res   probe.Result
}

// sharing is the registry of recent executions, safe for any goroutine.
type sharing struct {
	mu sync.Mutex
	m  map[string]*flight
}

// shareKey identifies what a probe sends and how its answer is judged:
// everything in the spec but the monitor's name and the share flag.
// Secret values are part of the options, so it is a hash.
func shareKey(s probe.Spec) string {
	h := sha256.New()
	w := func(v string) { h.Write([]byte(v)); h.Write([]byte{0}) }
	w(s.Type)
	w(s.Target)
	w(strconv.FormatInt(int64(s.Timeout), 10))
	w(string(s.Dialect))
	keys := make([]string, 0, len(s.Opts))
	for k := range s.Opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w(k)
		w(s.Opts[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// probe runs spec, or — when share is set — returns the result of an
// identical execution that started within window (waiting for it if it is
// still running). Every execution is recorded so that others can share it.
func (sh *sharing) probe(ctx context.Context, run func(context.Context, probe.Spec) probe.Result, spec probe.Spec, share bool, window time.Duration, now time.Time) probe.Result {
	if window <= 0 {
		return run(ctx, spec)
	}
	key := shareKey(spec)
	sh.mu.Lock()
	if sh.m == nil {
		sh.m = map[string]*flight{}
	}
	if f := sh.m[key]; share && f != nil && f.name != spec.Name && now.Sub(f.start) < window && !now.Before(f.start) {
		sh.mu.Unlock()
		select {
		case <-f.done:
			return shared(f.res)
		case <-ctx.Done():
			return probe.Result{Vote: probe.VoteAbstain, Reason: probe.ReasonInternal, Detail: "the shared check did not finish in time"}
		}
	}
	for k, f := range sh.m {
		if now.Sub(f.start) >= window {
			select {
			case <-f.done:
				delete(sh.m, k)
			default:
			}
		}
	}
	f := &flight{name: spec.Name, start: now, done: make(chan struct{})}
	sh.m[key] = f
	sh.mu.Unlock()
	f.res = run(ctx, spec)
	close(f.done)
	return f.res
}

// shared copies a result for another monitor; the evidence is copied too,
// so that nothing done with one monitor's result reaches the other's.
func shared(r probe.Result) probe.Result {
	if r.Evidence != nil {
		ev := *r.Evidence
		r.Evidence = &ev
	}
	if r.Metrics != nil {
		m := make(map[string]float64, len(r.Metrics))
		for k, v := range r.Metrics {
			m[k] = v
		}
		r.Metrics = m
	}
	return r
}
