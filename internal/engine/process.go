package engine

import (
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// onResult takes one finished local probe and judges it immediately.
func (e *Engine) onResult(rr roundResult) {
	rt := e.states[rr.name]
	m := e.monitors[rr.name]
	if rt != nil && !rr.push {
		rt.Running = false
	}
	if rt == nil || m == nil || m.DefHash != rr.defHash {
		return // removed or redefined while the round ran
	}
	nv := e.judgeLocal(rt, m.Scope, m.Probe, rr)
	e.judge(m, rt, nv, rr.res, rr.end, rr.due, rr.push)
}

// judgeLocal turns this node's probe result into its vote and feeds the
// local network detector. Rounds that overlapped a suspend or stall,
// failures on paths this host itself cannot reach and failures the pre-HARD
// self-check blamed on us become abstentions, so a problem on our side never
// counts against the target. Agents run it on their own results too.
func (e *Engine) judgeLocal(rt *monRuntime, scope netscope.Scope, spec probe.Spec, rr roundResult) NodeVote {
	now := e.now()
	res := rr.res
	if !rr.push {
		obs := observationFromResult(rr.end, spec, res, netscope.WAN)
		rt.Family, rt.Domain, rt.ByName = obs.Family, obs.Domain, obs.ByName
		if scope == "" || scope == "auto" {
			scope = e.inferScope(spec, res)
		}
		obs.Scope = scope
		rt.OnLink = e.onLink(res)
		obs.OnLink = rt.OnLink
		e.localnet.Observe(obs)
		for _, g := range rr.gateObs {
			e.localnet.Observe(g)
		}
	} else if rt.PushScope != "" {
		scope = rt.PushScope
	}
	rt.Scope = scope

	vote, reason, excluded := res.Vote, res.Reason, false
	if vote != probe.VoteOK {
		switch {
		case !rr.push && e.overlapsInterruption(rr.start, rr.end) != "":
			vote, reason = probe.VoteAbstain, e.overlapsInterruption(rr.start, rr.end)
		case probe.NetworkClass(reason) && e.watchdog.InGrace(now):
			vote, reason = probe.VoteAbstain, InterruptSuspend
		case rr.gateLocal:
			vote, reason, excluded = probe.VoteAbstain, probe.ReasonLocalNetwork, true
			e.deps.Sink.Counter(CounterOutageSaved, 1)
		default:
			if ex, why := e.localnet.Affects(scope, famOr(rt.Family), rt.OnLink, rt.ByName); ex {
				vote, reason, excluded = probe.VoteAbstain, why, true
			} else if probe.NetworkClass(reason) && !rr.push && e.localnet.Pending(scope, rt.Family, now) {
				vote, reason, excluded = probe.VoteAbstain, probe.ReasonLocalNetwork, true
			}
		}
	}
	return NodeVote{Node: e.self, Online: true, Present: true, Vote: vote, Reason: reason, Excluded: excluded}
}

// judge drives only this site's state machine, incidents and evidence.
func (e *Engine) judge(m *MonitorSpec, rt *monRuntime, nv NodeVote, res probe.Result, end, due time.Time, push bool) {
	now := e.now()
	v := LocalVerdict(nv)
	reason := nv.Reason
	slow := res.Vote == probe.VoteOK && m.SlowAfter > 0 && res.Latency > m.SlowAfter
	t := Step(rt.State, v, StepParams{
		Attempts:         m.Attempts,
		RecoverAttempts:  m.RecoverAttempts,
		DegradedAttempts: m.DegradedAttempts,
		Slow:             slow,
		OtherDegraded:    res.Degraded,
	})
	rt.State = t.To
	if rt.State.UnknownReason == UnknownLocalNetwork && e.outageSeen != nil {
		e.outageSeen[m.Name] = true
	}
	rt.LastCheck = end
	rt.LastResult = res
	rt.LastVerdict = v
	rt.LastReason, rt.LastDetail = reason, res.Detail
	if v.Kind == VerdictOK {
		rt.LastReason = ""
	}
	rt.LastVotes = nil
	if t.Changed() {
		rt.LastChange = now
	}

	status, excluded := t.To.Display(), v.Kind == VerdictUnknown
	if e.inMaint(m.Name) {
		// Planned work does not count against availability.
		status, excluded = StatusMaintenance, true
	}
	e.deps.Sink.Verdict(VerdictRecord{
		Monitor: m.Name, At: end, Verdict: v.Kind, Status: status, Hard: t.To.Hard,
		Reason: reason, LatencyMS: ms(res.Latency), FailNodes: v.FailingNodes, OKNodes: v.OKNodes,
		Excluded: excluded, Interval: e.intervalFor(m, rt),
	})
	if res.Evidence != nil {
		e.deps.Sink.Evidence(m.Name, e.self, end, res)
	}
	e.recordSiteObservation(m, rt, nv, res, end)
	e.onTransition(m, rt, t, v, now)
	e.afterJudge(m, rt, t, now)

	if t.Changed() || t.PartialStarted || t.PartialEnded {
		e.saveState(m.Name, rt)
	}
	if !push && !m.Paused {
		next, missed := NextDue(due, now, e.intervalFor(m, rt))
		if t.SoftFailure && m.Retry > 0 && m.Retry < m.Interval {
			next = now.Add(m.Retry)
		}
		// Rounds missed because this process was stopped or the machine
		// paused are not an overloaded host.
		if _, paused := e.watchdog.Overlaps(due, now); !paused {
			for i := 0; i < missed; i++ {
				e.watchdog.RecordRound(now, true)
			}
		}
		e.schedule(m.Name, next)
	}
}

// schedule sets the next due time of a monitor.
func (e *Engine) schedule(name string, due time.Time) {
	if rt := e.states[name]; rt != nil {
		rt.Due = due
	}
	e.sched.Set(name, due)
}

// overlapsInterruption returns the abstain reason when a round overlapped a
// suspend or stall of this process.
func (e *Engine) overlapsInterruption(start, end time.Time) string {
	if in, ok := e.watchdog.Overlaps(start, end); ok {
		return in.Kind
	}
	return ""
}

// inferScope classifies a monitor with scope=auto from the address it
// actually reached (or its host name when resolution failed).
func (e *Engine) inferScope(spec probe.Spec, r probe.Result) netscope.Scope {
	if r.Evidence != nil && r.Evidence.Peer != "" {
		if ap, err := parsePeer(r.Evidence.Peer); err == nil {
			src := netscope.SourceFor(ap.Addr())
			return netscope.Route(ap.Addr(), src, e.ifaces())
		}
	}
	if s, known := netscope.OfHost(targetHost(spec)); known {
		return s
	}
	if (strings.EqualFold(spec.Type, "EXEC") || strings.EqualFold(spec.Type, "DOCKER")) && spec.Opts["address"] == "" {
		return netscope.LAN // a local check, not a path to the internet
	}
	return netscope.WAN
}

// onLink reports whether the reached address is in a directly connected
// prefix (not routed via the gateway).
func (e *Engine) onLink(r probe.Result) bool {
	if r.Evidence == nil || r.Evidence.Peer == "" {
		return false
	}
	ap, err := parsePeer(r.Evidence.Peer)
	if err != nil {
		return false
	}
	for _, ifc := range e.ifaces() {
		if netscope.IsTunnel(ifc.Name) {
			continue
		}
		for _, p := range ifc.Prefixes {
			if p.Contains(ap.Addr().Unmap()) {
				return true
			}
		}
	}
	return false
}

// ifaces caches the interface list for a short while.
func (e *Engine) ifaces() []netscope.Iface {
	now := e.now()
	if e.ifCache == nil || now.Sub(e.ifCacheAt) > 30*time.Second {
		e.ifCache, e.ifCacheAt = netscope.Interfaces(), now
	}
	return e.ifCache
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// onTransition turns a state transition into incidents, notifications,
// events and value counters.
func (e *Engine) onTransition(m *MonitorSpec, rt *monRuntime, t Transition, v Verdict, now time.Time) {
	if t.SoftCleared {
		e.deps.Sink.Counter(CounterSoftFiltered, 1)
	}
	if t.Changed() && m.Handler != "" {
		e.runHandler(m, t, rt)
	}
	if t.Changed() {
		e.event(Event{Type: EventState, Severity: severityOf(t.To.Display()), Monitor: m.Name,
			Message: t.From.Display() + " → " + t.To.Display(),
			Data:    map[string]any{"msg": "state", "from": t.From.Display(), "to": t.To.Display(), "hard": t.To.Hard, "reason": rt.LastReason, "attempt": t.To.Attempt}})
	}
	if t.PartialStarted || t.PartialEnded {
		msg, text := "partial_started", "failing from some nodes: "+strings.Join(v.FailingNodes, ", ")
		if !t.To.Partial {
			msg, text = "partial_ended", "all nodes agree again"
		}
		e.event(Event{Type: EventPartial, Severity: "info", Monitor: m.Name, Message: text,
			Data: map[string]any{"msg": msg, "nodes": strings.Join(v.FailingNodes, ", "), "partial": t.To.Partial, "fail_nodes": v.FailingNodes}})
	}
	pol := e.policy(m.Policy)
	switch {
	case t.EnterHard:
		e.childFailed(m, rt, pol, now)
	case t.Recovered:
		e.monitorRecovered(m, rt, pol, now)
	case t.To.Avail == StatusCritical && t.To.Hard:
		if inc := e.byMonitor[m.Name]; inc != nil && inc.State.Open() {
			if ReasonChanged(&inc.State, reasonClass(rt.LastReason), pol.Notify) {
				e.event(Event{Type: EventAck, Severity: "info", Monitor: m.Name, Message: "ACK cleared: failure reason changed", Data: map[string]any{"msg": "ack_cleared"}})
			}
			inc.Reason, inc.Detail = rt.LastReason, rt.LastDetail
			e.saveIncident(inc)
		}
	}
	// Low-priority incident for the performance dimension.
	if pol.Notify.NotifyDegraded {
		if t.DegradedStarted {
			e.openLow(m, rt, pol, now)
		}
		switch inc := e.lowByMon[m.Name]; {
		case inc == nil || !inc.State.Open():
		case t.To.Avail == StatusCritical:
			// Slow became down: the failure incident takes over. Saying
			// "degraded ended" now would read like good news.
			e.supersedeLow(m.Name, inc, now)
		case t.DegradedEnded:
			Recover(&inc.State, now, pol.Notify)
			e.saveIncident(inc)
		}
	}
}

func severityOf(status string) string {
	switch status {
	case StatusCritical:
		return "critical"
	case StatusDegraded, StatusUnknown:
		return "warning"
	}
	return "info"
}

// reasonClass groups failure reasons for the ack-clear-on-change rule: an
// ACK given for "unreachable" does not cover "wrong content".
func reasonClass(reason string) string {
	switch {
	case reason == "":
		return ""
	case probe.NetworkClass(reason), reason == probe.ReasonConnRefused, reason == probe.ReasonPortUnreachable, reason == probe.ReasonFiltered:
		return "network"
	case reason == probe.ReasonTLS:
		return "tls"
	}
	return "protocol"
}
