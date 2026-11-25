package engine

import "time"

// Dependencies: a monitor with parents (parent=) that fails while one of
// its parents is down is UNREACHABLE: the failure is explained by the
// parent and not notified. When a child fails, the parents are checked
// again first, because a parent that is down but not yet re-checked
// still looks fine. The child's notification is held until every parent
// has been checked since the child failed, or until dependency-wait has
// passed.

// depHold is a child's held failure.
type depHold struct {
	mark  time.Time // parents (and, after a parent recovered, the child) must be checked after this
	until time.Time // give up waiting and notify
}

// childFailed replaces monitorFailed for a monitor entering HARD CRITICAL.
func (e *Engine) childFailed(m *MonitorSpec, rt *monRuntime, pol PolicySpec, now time.Time) {
	if e.inMaint(m.Name) {
		// Notified when the window closes if it is still down then.
		e.event(Event{Type: EventState, Severity: "info", Monitor: m.Name, Message: "failure during maintenance: not notified", Data: map[string]any{"msg": "maint_failure"}})
		return
	}
	if len(m.Parents) == 0 {
		e.monitorFailed(m, rt, pol, now)
		return
	}
	rt.Dep = &depHold{mark: rt.LastCheck, until: now.Add(e.depWait(m))}
	for _, p := range m.Parents {
		if prt := e.states[p]; prt != nil && prt.LastCheck.Before(rt.Dep.mark) && !prt.Running {
			e.schedule(p, now)
		}
	}
	e.evalDep(m, rt, now)
}

// depWait is how long a held child waits for its parents' verdict: the
// longest parent interval plus the dependency-wait tunable.
func (e *Engine) depWait(m *MonitorSpec) time.Duration {
	w := e.cfg.Tunables.Duration("engine.dependency-wait")
	longest := time.Duration(0)
	for _, p := range m.Parents {
		if pm := e.monitors[p]; pm != nil && pm.Interval > longest {
			longest = pm.Interval
		}
	}
	return longest + w
}

// parentDown names a parent that is CRITICAL (soft or hard) or itself
// unreachable, "" when none is.
func (e *Engine) parentDown(m *MonitorSpec) string {
	for _, p := range m.Parents {
		prt := e.states[p]
		if prt == nil {
			continue
		}
		if prt.State.Avail == StatusCritical || prt.Unreachable {
			return p
		}
	}
	return ""
}

// evalDep decides a held child: unreachable while a parent is down,
// notified once the parents are known to be fine.
func (e *Engine) evalDep(m *MonitorSpec, rt *monRuntime, now time.Time) {
	h := rt.Dep
	if h == nil {
		return
	}
	if rt.State.Avail != StatusCritical || !rt.State.Hard {
		// Recovered (or no longer judged) while held: nothing to say.
		e.setUnreachable(m, rt, "", false)
		rt.Dep = nil
		return
	}
	if p := e.parentDown(m); p != "" {
		e.setUnreachable(m, rt, p, true)
		return
	}
	fresh := !rt.LastCheck.Before(h.mark)
	for _, p := range m.Parents {
		if prt := e.states[p]; prt != nil && prt.LastCheck.Before(h.mark) {
			fresh = false
		}
	}
	if !fresh && now.Before(h.until) {
		return
	}
	e.setUnreachable(m, rt, "", false)
	rt.Dep = nil
	if e.inMaint(m.Name) {
		return // decided again when the window closes
	}
	e.monitorFailed(m, rt, e.policy(m.Policy), now)
}

func (e *Engine) setUnreachable(m *MonitorSpec, rt *monRuntime, parent string, on bool) {
	if rt.Unreachable == on {
		return
	}
	rt.Unreachable = on
	if on {
		e.event(Event{Type: EventState, Severity: "info", Monitor: m.Name, Message: "unreachable: parent " + parent + " is down",
			Data: map[string]any{"msg": "unreachable", "to": StatusUnreachable, "parent": parent}})
	}
	e.saveState(m.Name, rt)
}

// afterJudge re-evaluates the children held on a monitor that was just
// judged. A parent that recovered gives its unreachable children one more
// check of their own before they may notify.
func (e *Engine) afterJudge(m *MonitorSpec, rt *monRuntime, t Transition, now time.Time) {
	// A hold is not persisted: after a restart a child can be HARD
	// CRITICAL with no incident. Decide it again.
	if len(m.Parents) > 0 && rt.Dep == nil && !t.EnterHard && rt.State.Avail == StatusCritical && rt.State.Hard {
		if inc := e.byMonitor[m.Name]; inc == nil || !inc.State.Open() {
			e.childFailed(m, rt, e.policy(m.Policy), now)
		}
	}
	if rt.Dep != nil {
		e.evalDep(m, rt, now)
	}
	for _, c := range e.children[m.Name] {
		cm, crt := e.monitors[c], e.states[c]
		if cm == nil || crt == nil || crt.Dep == nil {
			continue
		}
		if crt.Unreachable && rt.State.Avail != StatusCritical && !rt.Unreachable {
			crt.Dep.mark, crt.Dep.until = rt.LastCheck, now.Add(cm.Interval+e.depWait(cm))
			if !crt.Running {
				e.schedule(c, now)
			}
		}
		e.evalDep(cm, crt, now)
	}
}

// expireDeps lets held children notify once they waited long enough.
func (e *Engine) expireDeps(now time.Time) {
	for name, rt := range e.states {
		if rt.Dep != nil && !now.Before(rt.Dep.until) {
			if m := e.monitors[name]; m != nil {
				e.evalDep(m, rt, now)
			}
		}
	}
}

// indexChildren maps each monitor to the monitors that depend on it.
func indexChildren(monitors map[string]*MonitorSpec) map[string][]string {
	out := map[string][]string{}
	for name, m := range monitors {
		for _, p := range m.Parents {
			out[p] = append(out[p], name)
		}
	}
	return out
}
