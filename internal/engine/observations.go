package engine

import (
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/observation"
	"github.com/githubflyideas/unagikuma/internal/probe"
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

// observationReadWait is the longest one alert waits for the sites'
// observations of its monitor. The read is made where the alert is sent,
// not in the engine loop, so the wait holds back that one alert only —
// never the checks or other alerts — and a normal read takes far less.
const observationReadWait = 3 * time.Second

// obsFetch is the sites' observations an alert should carry, read once
// where the alert is sent (and shared by every channel it goes to).
type obsFetch struct {
	read    func() (*observation.Snapshot, error)
	since   time.Time
	gateway string
	uplink  string
	wait    time.Duration
	log     func(msg string, args ...any)
	once    sync.Once
	snap    *observation.Snapshot
}

// get reads the observations, waiting at most f.wait; nil when they are
// not there in time (the alert then goes without them).
func (f *obsFetch) get() *observation.Snapshot {
	f.once.Do(func() {
		type result struct {
			snap *observation.Snapshot
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			snap, err := f.read()
			ch <- result{snap, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				f.log("observation snapshot unavailable", "err", r.err)
				return
			}
			if r.snap != nil {
				r.snap.Since, r.snap.Gateway, r.snap.Uplink = f.since, f.gateway, f.uplink
			}
			f.snap = r.snap
		case <-time.After(f.wait):
			f.log("observation snapshot too slow: alert sent without it")
		}
	})
	return f.snap
}

// observationFetch prepares, in the engine loop, what an alert about a
// monitor needs to read its sites' observations later (nil outside a
// cluster). Nothing is read here.
func (e *Engine) observationFetch(name string) *obsFetch {
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
	sites, def, now := e.cfg.ObservationSites[name], m.DefHash, e.now()
	f := &obsFetch{
		read: func() (*observation.Snapshot, error) { return s.SiteObservationSnapshot(name, def, sites, now) },
		wait: observationReadWait,
		log:  func(msg string, args ...any) { e.log.Warn(msg, append([]any{"monitor", name}, args...)...) },
	}
	if rt := e.states[name]; rt != nil {
		f.since = rt.FailSince
	}
	h := e.localnet.Health(now)
	f.gateway, f.uplink = h.GatewayState, h.Uplink
	if f.gateway == "" {
		f.gateway = "unknown"
	}
	return f
}
