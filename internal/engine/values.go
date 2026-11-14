package engine

import (
	"maps"
	"time"

	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// Value rules (warn=, crit=, change= and the rest) judge the number a
// check reads. A critical value fails the check like any other failure; a
// warning makes it degraded. The samples behind a change rule live here,
// per monitor, and start over when the monitor is redefined.

type valueState struct {
	defHash string
	tr      *valuerule.Tracker // nil: the monitor has no value rules
	last    valuerule.Outcome
}

// valueTracker returns the tracker of a monitor, or nil when it has no
// value rules.
func (e *Engine) valueTracker(m *MonitorSpec) *valueState {
	if e.values == nil {
		e.values = map[string]*valueState{}
	}
	if vs, ok := e.values[m.Name]; ok && vs.defHash == m.DefHash {
		return vs
	}
	vs := &valueState{defHash: m.DefHash}
	e.values[m.Name] = vs
	rules, err := valuerule.Parse(func(k string) string { return m.Probe.Opts[k] }, probe.ValueUnit(m.Probe))
	if err == nil && rules.Active() {
		vs.tr = &valuerule.Tracker{Rules: rules}
		if s := e.preload[m.Name]; len(s) > 0 {
			vs.tr.Load(s) // kept from before a restart
		}
	}
	delete(e.preload, m.Name)
	return vs
}

// LoadValues gives the engine the values kept before a restart, by
// monitor, oldest first. Call it before Run.
func (e *Engine) LoadValues(v map[string][]valuerule.Sample) { e.preload = v }

// applyValues judges the value in a successful local result.
func (e *Engine) applyValues(m *MonitorSpec, res probe.Result, at time.Time) probe.Result {
	if res.Vote != probe.VoteOK {
		return res // a failed check has no value to judge
	}
	vs := e.valueTracker(m)
	if vs.tr == nil {
		return res
	}
	vs.tr.Retain = e.cfg.Tunables.Duration("values.retain")
	vs.tr.Window = e.cfg.Tunables.Duration("values.median-window")
	out := vs.tr.Observe(at, res.Metrics)
	vs.last = out
	if out.Sampled {
		e.deps.Sink.Value(m.Name, at, out.Value)
	}
	res.Metrics = maps.Clone(res.Metrics)
	if res.Metrics == nil {
		res.Metrics = map[string]float64{}
	}
	if out.Sampled {
		res.Metrics["value.current"] = out.Value
	}
	if out.HasRef {
		res.Metrics["value.ref"] = out.Ref
	}
	if out.Level == valuerule.Learning {
		res.Metrics["value.learning_s"] = out.LearningLeft.Seconds()
	}
	switch out.Level {
	case valuerule.Crit:
		res.Vote, res.Reason, res.Detail = probe.VoteFail, probe.ReasonValueBreach, out.Detail
	case valuerule.Warn:
		res.Degraded, res.Detail = true, out.Detail
	case valuerule.Missing:
		// Rules that can never fire must not look like they are watching.
		res.Degraded, res.Detail = true, out.Detail
	default:
		if out.Detail != "" {
			if res.Detail != "" {
				res.Detail += " · "
			}
			res.Detail += out.Detail
		}
	}
	return res
}
