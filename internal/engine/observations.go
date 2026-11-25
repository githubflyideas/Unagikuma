package engine

import (
	"github.com/githubflyideas/unagikuma/internal/observation"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"time"
)

type observationSink interface {
	RecordSiteObservation(observation.Row) error
	SiteObservationSnapshot(string, string, []string, time.Time) (*observation.Snapshot, error)
}

func (e *Engine) recordSiteObservation(m *MonitorSpec, rt *monRuntime, nv NodeVote, res probe.Result, at time.Time) {
	s, ok := e.deps.Sink.(observationSink)
	if !ok || e.cfg.SourceNode == "" {
		return
	}
	status := "unknown"
	v := LocalVerdict(nv)
	switch v.Kind {
	case VerdictOK:
		status = "up"
	case VerdictCritical:
		status = "down"
	}
	interval := e.intervalFor(m, rt)
	if m.Push {
		interval = m.PushPeriod + m.PushGrace
	}
	if interval <= 0 {
		interval = time.Minute
	}
	err := s.RecordSiteObservation(observation.Row{MonitorID: m.Name, NodeID: e.cfg.SourceNode, Status: status, CheckedAt: at, LatencyMS: ms(res.Latency), Error: nv.Reason, IntervalMS: interval.Milliseconds(), DefHash: m.DefHash})
	if err != nil {
		e.log.Error("persist local observation", "monitor", m.Name, "err", err)
	}
}

// observationReadWait is the longest an alert waits for the sites'
// observations of its monitor.
const observationReadWait = 50 * time.Millisecond

func (e *Engine) siteObservationSnapshot(name string) *observation.Snapshot {
	if len(e.cfg.Nodes) == 0 {
		return nil
	}
	m := e.monitors[name]
	if m == nil {
		return nil
	}
	s, ok := e.deps.Sink.(observationSink)
	if !ok {
		return nil
	}
	// An alert never waits for this: the read gets observationReadWait,
	// and an alert built without it simply has no observation line.
	type result struct {
		snap *observation.Snapshot
		err  error
	}
	ch := make(chan result, 1)
	sites, def, now := e.cfg.ObservationSites[name], m.DefHash, e.now()
	go func() {
		snap, err := s.SiteObservationSnapshot(name, def, sites, now)
		ch <- result{snap, err}
	}()
	var snap *observation.Snapshot
	select {
	case r := <-ch:
		if r.err != nil {
			e.log.Warn("observation snapshot unavailable", "err", r.err)
			return nil
		}
		snap = r.snap
	case <-time.After(observationReadWait):
		e.log.Warn("observation snapshot too slow: alert sent without it", "monitor", name)
		return nil
	}
	if snap != nil {
		if rt := e.states[name]; rt != nil {
			snap.Since = rt.FailSince
		}
	}
	if snap != nil {
		h := e.localnet.Health(e.now())
		snap.Gateway = h.GatewayState
		snap.Uplink = h.Uplink
		if snap.Gateway == "" {
			snap.Gateway = "unknown"
		}
	}
	return snap
}
