package engine

import (
	"fmt"
	"net/netip"
	"time"
)

// Apply installs a new configuration: monitors are added, redefined,
// rescheduled or removed; policies, channels and tunables are replaced.
// It is safe to call before Run (startup) and while running (hot reload).
func (e *Engine) Apply(cfg Config) {
	if !e.started.Load() {
		e.apply(cfg)
		return
	}
	e.do(func() { e.apply(cfg) })
}

func (e *Engine) apply(cfg Config) {
	now := e.now()
	first := !e.applied
	e.applied = true
	oldMon := e.monitors
	e.cfg = cfg
	if e.cfg.Location == nil {
		e.cfg.Location = time.Local
	}
	e.applyTunables(cfg.Tunables)
	e.applyAnchors(cfg.Anchors)
	e.self = NodeName
	if cfg.Self != "" && len(cfg.Nodes) > 0 {
		e.self = cfg.Self
	}
	e.applyNodes(cfg.Nodes, now)

	ts := cfg.Tunables
	firstDelay := ts.Duration("engine.first-round-delay")
	spread := ts.Duration("suspend.recheck-spread")
	e.monitors = make(map[string]*MonitorSpec, len(cfg.Monitors))
	for i := range cfg.Monitors {
		m := &cfg.Monitors[i]
		e.monitors[m.Name] = m
		rt := e.states[m.Name]
		_, existed := oldMon[m.Name]
		switch {
		case rt == nil:
			rt = &monRuntime{State: NewState(), DefHash: m.DefHash, Since: now}
			e.states[m.Name] = rt
			e.saveState(m.Name, rt)
		case rt.DefHash != m.DefHash:
			// The check itself changed and is run again right away. An open
			// problem stays open meanwhile: tuning a check that is failing
			// (a timeout, an expected keyword) must not close the incident
			// and page again for the same outage. If the new check still
			// fails the incident simply continues; if it passes, the usual
			// recovery closes it. Without an open problem the monitor starts
			// over as PENDING, since old results say nothing about the new
			// definition.
			if !e.hasOpenIncident(m.Name) {
				rt.State, rt.LastChange = NewState(), now
			}
			rt.DefHash, rt.Since = m.DefHash, now
			e.saveState(m.Name, rt)
			existed = false
		}
		if old := oldMon[m.Name]; old != nil && old.Paused && !m.Paused {
			rt.Since = now // resumed: watched again from now
		}
		if m.Paused || m.Push {
			e.sched.Remove(m.Name)
			continue
		}
		if rt.Running {
			continue // rescheduled when the round completes
		}
		var due time.Time
		switch {
		case first && rt.State.Avail == StatusCritical:
			// Restart: open problems are re-checked right away.
			due = now.Add(firstDelay)
		case first:
			due = now.Add(firstDelay + Spread(m.Name, min(m.Interval, spread)))
		case !existed:
			due = now.Add(firstDelay)
		default:
			due = rt.LastCheck.Add(e.intervalFor(m, rt))
			if old, ok := e.sched.Due(m.Name); ok && old.Before(due) {
				due = old
			}
			if due.Before(now) {
				due = now
			}
		}
		e.sched.Set(m.Name, due)
	}
	e.children = indexChildren(e.monitors)
	if e.maintOf == nil {
		e.maintOf, e.maintOpen = map[string]string{}, map[string]time.Time{}
	}
	e.checkMaint(now)
	for name := range oldMon {
		if _, ok := e.monitors[name]; ok {
			continue
		}
		e.sched.Remove(name)
		e.closeMonitorIncidents(name, "deleted")
		delete(e.states, name)
		e.deps.Sink.DeleteState(name)
	}
	// Restored states of monitors that no longer exist.
	for name := range e.states {
		if _, ok := e.monitors[name]; !ok {
			delete(e.states, name)
			e.deps.Sink.DeleteState(name)
		}
	}
	if !first {
		e.event(Event{Type: EventConfig, Severity: "info", Message: fmt.Sprintf("configuration %d applied", cfg.Serial), Data: map[string]any{"msg": "config_applied", "serial": cfg.Serial, "monitors": len(cfg.Monitors)}})
	}
	e.scheduleReminder(now)
	e.scheduleReport(now)
}

// RenameMonitor carries state, schedule and incidents over to a new name
// (a rename is not a redefinition). Call before Apply of the renamed config.
func (e *Engine) RenameMonitor(oldName, newName string) {
	f := func() {
		rt, ok := e.states[oldName]
		if !ok {
			return
		}
		delete(e.states, oldName)
		e.states[newName] = rt
		e.sched.Rename(oldName, newName)
		for _, m := range []map[string]*Incident{e.byMonitor, e.lowByMon, e.regional} {
			if inc, ok := m[oldName]; ok {
				delete(m, oldName)
				inc.Monitor = newName
				m[newName] = inc
				e.saveIncident(inc)
			}
		}
		if spec, ok := e.monitors[oldName]; ok {
			delete(e.monitors, oldName)
			spec.Name = newName
			e.monitors[newName] = spec
		}
		e.deps.Sink.DeleteState(oldName)
		e.saveState(newName, rt)
	}
	if !e.started.Load() {
		f()
		return
	}
	e.do(f)
}

func (e *Engine) applyAnchors(anchors []AnchorSpec) {
	want := map[string]AnchorSpec{}
	for _, a := range anchors {
		want[string(a.Kind)+"|"+a.Addr] = a
	}
	for k, a := range e.anchors {
		if _, ok := want[k]; !ok {
			e.localnet.RemoveAnchor(a.Kind, anchorFamily(a.Addr), a.Addr)
		}
	}
	e.anchors = want
}

func anchorFamily(addr string) Family {
	if a, err := parseAddr(addr); err == nil {
		return FamilyOf(a)
	}
	return FamilyV4
}

// intervalFor is the interval until the next round: the retry interval
// while a problem is still SOFT.
func (e *Engine) intervalFor(m *MonitorSpec, rt *monRuntime) time.Duration {
	if rt.State.Avail == StatusCritical && !rt.State.Hard && m.Retry > 0 {
		return m.Retry
	}
	return m.Interval
}

// Restore seeds the engine with persisted state before the first Apply.
func (e *Engine) Restore(states []StateRecord, incidents []*Incident, intents []*Intent) {
	for _, s := range states {
		e.states[s.Name] = &monRuntime{State: s.State, DefHash: s.DefHash, LastCheck: s.LastCheck, LastChange: s.LastChange,
			LastReason: s.Reason, LastDetail: s.Detail}
	}
	for _, inc := range incidents {
		if inc.State.Closed {
			continue
		}
		e.incidents[inc.ID] = inc
		switch inc.Kind {
		case IncidentNormal:
			e.byMonitor[inc.Monitor] = inc
		case IncidentLow:
			e.lowByMon[inc.Monitor] = inc
		case IncidentRegional:
			e.regional[inc.Monitor] = inc
		case IncidentSystem:
			e.system[inc.System] = inc
		}
	}
	now := e.now()
	byIncident := map[int64][]*Intent{}
	for _, in := range intents {
		if in.State != IntentPending {
			continue
		}
		in.Delivery.Recover()
		e.intents[in.ID] = in
		byIncident[in.IncidentID] = append(byIncident[in.IncidentID], in)
	}
	// Problems that started and ended while unagikuma was down are told as
	// one message instead of a stale alarm followed by a recovery.
	for id, ins := range byIncident {
		inc := e.incidents[id]
		if id == 0 || inc == nil || inc.State.ResolvedAt.IsZero() {
			continue
		}
		var problem, recovery *Intent
		for _, in := range ins {
			switch in.Kind {
			case string(NoticeProblem), string(NoticeRepeat), string(NoticeReopen):
				if problem == nil || in.CreatedAt.Before(problem.CreatedAt) {
					problem = in
				}
			case string(NoticeRecovery):
				recovery = in
			}
		}
		if problem == nil || recovery == nil || problem.Delivery.Pos() > 0 {
			continue
		}
		for _, in := range ins {
			if in != recovery && in.Kind != string(NoticeRecovery) {
				in.State, in.UpdatedAt = IntentMerged, now
				_ = e.deps.Sink.SaveIntent(in)
				delete(e.intents, in.ID)
			}
		}
		recovery.Message.Body = e.text("failed_and_recovered_while_down", problem.Message.Time, inc.State.ResolvedAt)
		_ = e.deps.Sink.SaveIntent(recovery)
	}
}

// parseAddr parses an IP literal (brackets allowed).
func parseAddr(s string) (netip.Addr, error) {
	if len(s) > 1 && s[0] == '[' && s[len(s)-1] == ']' {
		s = s[1 : len(s)-1]
	}
	return netip.ParseAddr(s)
}
