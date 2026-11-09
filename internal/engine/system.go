package engine

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// StorageFailing opens the storage incident while writes to the data
// directory keep failing (since when, why, how many writes were lost) and
// resolves it once they succeed again.
func (e *Engine) StorageFailing(on bool, since time.Time, why string, lost int64) {
	e.do(func() {
		now := e.now()
		if on {
			e.openSystem(SystemStorage, now, e.text("storage_failing", since, why, lost))
			return
		}
		e.resolveSystem(SystemStorage, now, "")
	})
}

// SystemTwoMasters is the system incident of a permanent split: two
// masters of the same cluster are reachable at once.
const SystemTwoMasters = "two_masters"

// TwoMasters opens (or keeps) the CRITICAL notice that another master of
// this cluster is reachable. peer and term identify the other side.
func (e *Engine) TwoMasters(peer string, term uint64) {
	f := func() {
		now := e.now()
		detail := e.text("two_masters", peer, term)
		e.openSystemAs(SystemTwoMasters, "system", e.text("two_masters_title"), detail, now)
	}
	if !e.started.Load() {
		f()
		return
	}
	e.do(f)
}

// MasterUnreachable opens the replica's "checking on its own" incident
// while the master is out of reach and resolves it once it is back.
func (e *Engine) MasterUnreachable(on bool, master string, since time.Time) {
	e.do(func() {
		now := e.now()
		if on {
			e.openSystem(SystemMaster, now, e.text("master_unreachable", master, since))
			return
		}
		e.resolveSystem(SystemMaster, now, "")
	})
}

// openSystem opens (or keeps) a system incident; it is notified through the
// "system" policy.
func (e *Engine) openSystem(key string, now time.Time, detail string) *Incident {
	return e.openSystemAs(key, "system", e.text("system_title_"+key), detail, now)
}

// openSystemAs opens a system incident notified through the given policy.
func (e *Engine) openSystemAs(key, policy, title, detail string, now time.Time) *Incident {
	if inc := e.system[key]; inc != nil && inc.State.Open() {
		return inc
	}
	pol := e.policy(policy)
	prev := e.system[key]
	var prevState *IncidentState
	if prev != nil {
		prevState = &prev.State
	}
	inc := &Incident{Kind: IncidentSystem, System: key, Policy: pol.Name, Title: title, Reason: key, Detail: detail}
	np := e.effectivePolicy(inc, pol)
	st, notices, _ := Fail(prevState, now, key, np)
	if prev != nil && st == &prev.State {
		inc = prev
		inc.Detail = detail
	} else {
		inc.State = *st
		if prev != nil {
			e.retireIncident(prev)
		}
	}
	e.saveIncident(inc)
	e.system[key] = inc
	e.event(Event{Type: EventSystem, Severity: "critical", Message: inc.Title, Data: map[string]any{"audit": "system_opened", "system": key, "incident_id": inc.ID, "detail": detail}})
	e.notices(inc, notices, now)
	return inc
}

// resolveSystem resolves a system incident; summary is the body of the
// recovery notice.
func (e *Engine) resolveSystem(key string, now time.Time, summary string) {
	inc := e.system[key]
	if inc == nil || !inc.State.Open() {
		return
	}
	inc.Summary = summary
	notices := Recover(&inc.State, now, e.effectivePolicy(inc, e.policy(inc.Policy)))
	e.saveIncident(inc)
	e.event(Event{Type: EventSystem, Severity: "info", Message: inc.Title + " — resolved", Data: map[string]any{"msg": "system_resolved", "title": inc.Title, "incident_id": inc.ID, "duration_s": int(now.Sub(inc.State.OpenedAt).Seconds())}})
	e.notices(inc, notices, now)
}

// onLocalNet handles local network anomaly transitions.
func (e *Engine) onLocalNet(ev LocalNetEvent, now time.Time) {
	a := ev.Anomaly
	if ev.Enter {
		e.event(Event{Type: EventSystem, Severity: "warning", Message: e.text("anomaly_"+a.Kind, a.Detail, a.Suspected),
			Data: map[string]any{"kind": a.Kind, "family": int(a.Family), "since": a.Since, "domains": a.Domains, "suspected": a.Suspected}})
		e.outageDNS = len(e.localnet.Active()) == 1 && a.Kind == AnomalyDNS
		if len(e.localnet.Active()) == 1 {
			e.outage = nil // a relapse: the summary covers the whole of it later
			if e.outageSeen == nil {
				e.outageSeen = map[string]bool{}
			}
			e.backdate(a, now)
			e.openSystem(SystemLocalNetwork, now, e.text("anomaly_"+a.Kind, a.Detail, a.Suspected))
		}
		return
	}
	e.event(Event{Type: EventSystem, Severity: "info", Message: e.text("anomaly_end_" + a.Kind),
		Data: map[string]any{"kind": a.Kind, "since": a.Since, "duration_s": int(now.Sub(a.Since).Seconds())}})
	if len(e.localnet.Active()) > 0 {
		return
	}
	// Re-check the affected monitors right away with normal judgement.
	var names []string
	delay := e.cfg.Tunables.Duration("engine.first-round-delay")
	var timeout time.Duration
	seen := e.outageSeen
	e.outageSeen = nil
	for _, name := range e.sortedMonitorNames() {
		rt := e.states[name]
		if rt == nil || !(seen[name] || rt.State.Held || rt.State.Avail == StatusUnknown && rt.State.UnknownReason == UnknownLocalNetwork) {
			continue
		}
		names = append(names, name)
	}
	// The re-checks are spread so a link that just came back is not hit by
	// every monitor at once (more monitors, a wider spread, capped).
	spread := min(e.cfg.Tunables.Duration("localnet.recheck-spread"), time.Duration(len(names))*100*time.Millisecond)
	for _, name := range names {
		rt := e.states[name]
		if m := e.monitors[name]; m != nil && !m.Paused && !m.Push && !rt.Running {
			e.schedule(name, now.Add(delay+Spread(name, spread)))
			timeout = max(timeout, m.Probe.Timeout)
		}
	}
	var since time.Time
	if inc := e.system[SystemLocalNetwork]; inc != nil {
		since = inc.State.OpenedAt
	}
	// What was queued while nothing could be sent describes a network that
	// is back: drop it. The re-checks say what is really failing.
	dropped := e.dropQueued(since.Add(-e.cfg.Tunables.Duration("localnet.backdate")), now)
	if inc := e.system[SystemLocalNetwork]; inc != nil {
		// "The network is down", still queued, would arrive after it is back.
		e.expireNotices(inc, now)
	}
	// "Every channel failed" because nothing could get out is not a broken
	// channel either, when nobody was told about it.
	if inc := e.system[SystemAllFailed]; inc != nil && inc.State.Open() && !inc.State.OpenedAt.Before(since) && !inc.State.told() {
		e.closeSystemQuietly(SystemAllFailed, now, "")
	}
	wait := delay + spread + timeout + e.cfg.Tunables.Duration("engine.round-grace") + 2*time.Second
	back := now
	if !ev.Back.IsZero() && ev.Back.Before(now) && ev.Back.After(since) {
		back = ev.Back
	}
	e.outage = &outageEnd{since: since, end: now, back: back, dnsOnly: e.outageDNS, names: names, deadline: now.Add(wait), dropped: dropped}
}

// releaseHold fails open: a local outage has lasted longer than
// localnet.max-hold, so the monitors it holds back are judged normally
// again, in case it is not our network after all. One system notice says
// so; problem notices sent while it lasts carry a "may be wrong" note.
func (e *Engine) releaseHold(since, now time.Time) {
	held := 0
	spread := e.cfg.Tunables.Duration("localnet.recheck-spread")
	for _, name := range e.sortedMonitorNames() {
		rt := e.states[name]
		if rt == nil || !(rt.State.Held || rt.State.Avail == StatusUnknown && rt.State.UnknownReason == UnknownLocalNetwork) {
			continue
		}
		held++
		e.decide(Decision{At: now, Monitor: name, Step: LedReleased, Class: ClassNetwork, HoldStart: since, HoldEnd: now,
			Note: "local outage lasted longer than localnet.max-hold: judged normally again"})
		if m := e.monitors[name]; m != nil && !m.Paused && !m.Push && !rt.Running {
			e.schedule(name, now.Add(Spread(name, max(spread, time.Second))))
		}
	}
	body := e.text("hold_released", since, now.Sub(since), held)
	e.event(Event{Type: EventSystem, Severity: "warning", Message: body,
		Data: map[string]any{"msg": "hold_released", "since": since, "held": held}})
	e.systemNotice(now, body)
}

// outageEnd is a local outage that ended: its summary waits until the
// monitors it hid have been re-checked.
type outageEnd struct {
	since, end, deadline time.Time
	back                 time.Time // when the network was back (end less the exit confirmation)
	dnsOnly              bool      // only the resolver failed
	names                []string
	dropped              int
}

// finishOutage sends the summary of an ended local outage once its
// monitors have been re-checked (or the wait is over): one message, and
// only for an outage long enough to be worth one (localnet.notify-after).
func (e *Engine) finishOutage(now time.Time) {
	o := e.outage
	if o == nil {
		return
	}
	var failing []string
	for _, name := range o.names {
		rt := e.states[name]
		if rt == nil {
			continue
		}
		if now.Before(o.deadline) && !rt.LastCheck.After(o.end) {
			return // not re-checked yet
		}
		if rt.State.Avail == StatusCritical {
			failing = append(failing, name)
		}
	}
	e.outage = nil
	for _, name := range o.names {
		note := "held during the local outage; fine when re-checked"
		if slices.Contains(failing, name) {
			note = "held during the local outage; still failing when re-checked"
		}
		e.decide(Decision{At: now, Monitor: name, Step: LedHeld, Class: ClassNetwork, HoldStart: o.since, HoldEnd: o.back, Note: note})
	}
	summary := e.text("outage_summary", o.since, o.back, len(o.names), failing, o.dnsOnly)
	lost := e.takeUnsent(o.since.Add(-e.cfg.Tunables.Duration("localnet.backdate")))
	if lost != "" {
		summary += "\n" + e.text("outage_unsent", lost) // e.g. a crash during the outage
	}
	inc := e.system[SystemLocalNetwork]
	if inc == nil || !inc.State.Open() {
		return
	}
	e.event(Event{Type: EventSystem, Severity: "info", Message: summary,
		Data: map[string]any{"msg": "outage_summary", "duration_s": int(o.back.Sub(o.since).Seconds()), "affected": len(o.names), "failing": len(failing), "dropped": o.dropped, "dns_only": o.dnsOnly}})
	if inc.State.told() || o.back.Sub(o.since) < e.cfg.Tunables.Duration("localnet.notify-after") && lost == "" {
		// Told at the start (a channel that still worked): the recovery
		// carries the summary. A short outage ends quietly, unless a
		// notice (a crash meanwhile) could not be sent.
		e.resolveSystem(SystemLocalNetwork, now, summary)
		return
	}
	// Nobody could be told when it began: one message now, instead of a
	// late "it is down" followed by "it is back".
	e.closeSystemQuietly(SystemLocalNetwork, now, summary)
	e.systemNotice(now, summary)
}

// backdate gives a just recognised local outage what it caused before it
// was recognised: failures of the monitors it affects that began within
// localnet.backdate of its start and that nobody has been told about yet
// are taken back — no incident, no queued notice — and the monitors wait
// for the outage to end like the others.
func (e *Engine) backdate(a Anomaly, now time.Time) {
	cutoff := a.Since.Add(-e.cfg.Tunables.Duration("localnet.backdate"))
	taken := 0
	for _, name := range e.sortedMonitorNames() {
		rt := e.states[name]
		if rt == nil || rt.State.Avail != StatusCritical || reasonClass(rt.LastReason) != "network" || rt.LastChange.Before(cutoff) {
			continue
		}
		if ex, _ := e.localnet.Affects(rt.Scope, famOr(rt.Family), rt.OnLink, rt.ByName); !ex {
			continue
		}
		if inc := e.byMonitor[name]; inc != nil && inc.State.Open() {
			// A service that failed on its own (HTTP 500) and now times
			// out because of the cut did not fail with the outage.
			if inc.State.told() || inc.State.OpenedAt.Before(cutoff) || cmp.Or(inc.openClass, reasonClass(inc.Reason)) != "network" {
				continue // someone knows about it: it stays, held
			}
			e.closeMonitorIncidents(name, "local_network")
		}
		st := rt.State
		st.Avail, st.Hard, st.Attempt, st.OKStreak, st.Held, st.Partial = StatusUnknown, true, 0, 0, false, false
		st.UnknownReason = UnknownLocalNetwork
		rt.State, rt.LastChange = st, now
		e.saveState(name, rt)
		e.outageSeen[name] = true
		taken++
		e.decide(Decision{At: now, Monitor: name, Step: LedHeld, Class: ClassNetwork, HoldStart: a.Since,
			Evidence: map[string]any{"local_outage": string(a.Kind), "reason": rt.LastReason}, Note: "failure began with the local outage: taken back"})
	}
	// Notices that failed for lack of network are not a broken channel.
	if inc := e.system[SystemAllFailed]; inc != nil && inc.State.Open() && !inc.State.OpenedAt.Before(cutoff) && !inc.State.told() {
		e.closeSystemQuietly(SystemAllFailed, now, "")
	}
	if taken > 0 {
		e.event(Event{Type: EventSystem, Severity: "info", Message: fmt.Sprintf("%d failures began with the local outage and were taken back", taken),
			Data: map[string]any{"msg": "outage_backdated", "monitors": taken}})
	}
}

// dropQueued discards the undelivered monitor notices created since
// from, returning how many there were.
func (e *Engine) dropQueued(from, now time.Time) int {
	n := 0
	for id, in := range e.intents {
		if in.System || in.CreatedAt.Before(from) {
			continue
		}
		in.State, in.UpdatedAt = IntentDropped, now
		_ = e.deps.Sink.SaveIntent(in)
		delete(e.intents, id)
		e.decideIntent(in, LedDropped, "queued during a local outage: replaced by its summary")
		n++
	}
	return n
}

// closeSystemQuietly ends a system incident without a notice.
func (e *Engine) closeSystemQuietly(key string, now time.Time, summary string) {
	inc := e.system[key]
	if inc == nil {
		return
	}
	inc.Summary = summary
	if inc.State.ResolvedAt.IsZero() {
		inc.State.ResolvedAt = now
	}
	inc.State.Closed = true
	e.saveIncident(inc)
	e.retireIncident(inc)
	delete(e.system, key)
	e.event(Event{Type: EventSystem, Severity: "info", Message: inc.Title + " — resolved", Data: map[string]any{"msg": "system_resolved", "title": inc.Title, "incident_id": inc.ID, "duration_s": int(now.Sub(inc.State.OpenedAt).Seconds())}})
}

// onInterruption handles a detected suspend or stall.
func (e *Engine) onInterruption(in Interruption, now time.Time) {
	e.lastInterruption = in
	e.event(Event{Type: EventSystem, Severity: "warning", Message: e.text("interrupted_"+in.Kind, in.Duration),
		Data: map[string]any{"kind": in.Kind, "duration_s": int(in.Duration.Seconds())}})
	if in.Kind == InterruptSuspend {
		// Every monitor gets a fresh round soon after the resume, spread so
		// the wake-up is not a burst.
		spread := e.cfg.Tunables.Duration("suspend.recheck-spread")
		for _, name := range e.sortedMonitorNames() {
			if m := e.monitors[name]; m != nil && !m.Paused && !m.Push {
				if rt := e.states[name]; rt != nil && !rt.Running {
					e.schedule(name, now.Add(Spread(name, max(spread, time.Second))))
				}
			}
		}
	}
	if in.Notify {
		e.systemNotice(now, e.text("interrupted_"+in.Kind, in.Duration))
	}
}

// systemNotice sends a one-off message through the system policy.
// Its title is the notice itself (shortened), so a phone shows what
// happened instead of a generic "system notice".
func (e *Engine) systemNotice(now time.Time, body string) {
	e.enqueue(&Intent{Kind: "SYSTEM", Policy: e.policy("system").Name, CreatedAt: now, DueAt: now, System: true,
		Message: notify.Message{Kind: "SYSTEM", Priority: "high", Title: noticeTitle(body, e.text("system_notice_title")), Body: body, Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang}})
}

// noticeTitle is the first sentence of body, at most 90 characters.
func noticeTitle(body, fallback string) string {
	s, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	for _, sep := range []string{". ", "。", "; ", "；"} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	s = strings.TrimSpace(strings.TrimRight(s, ".。"))
	if r := []rune(s); len(r) > 90 {
		s = string(r[:89]) + "…"
	}
	if s == "" {
		return fallback
	}
	return s
}

// StartupNotice reports how the previous run ended: an abnormal exit (no
// clean shutdown marker) is notified; a clean restart only when
// suspend.notify-restart is on. lastBeat is the last liveness timestamp.
func (e *Engine) StartupNotice(clean bool, lastBeat time.Time) {
	f := func() {
		now := e.now()
		if clean {
			e.event(Event{Type: EventSystem, Severity: "info", Message: e.text("restarted"), Data: map[string]any{"msg": "restarted"}})
			if e.cfg.Tunables.Bool("suspend.notify-restart") {
				e.systemNotice(now, e.text("restarted"))
			}
			return
		}
		down := time.Duration(0)
		if !lastBeat.IsZero() {
			down = now.Sub(lastBeat)
		}
		msg := e.text("abnormal_exit", lastBeat, down)
		e.event(Event{Type: EventSystem, Severity: "critical", Message: msg, Data: map[string]any{"msg": "abnormal_exit", "down_s": int(down.Seconds())}})
		e.systemNotice(now, msg)
	}
	if !e.started.Load() {
		f()
		return
	}
	e.do(f)
}

// onOverload opens or resolves the "host overloaded" system incident.
func (e *Engine) onOverload(ev OverloadEvent, now time.Time) {
	if ev.Enter {
		e.openSystem(SystemOverload, now, e.text("overload", ev.Ratio, ev.Rounds))
		return
	}
	e.resolveSystem(SystemOverload, now, "")
}

// Push records a call to a PUSH monitor's URL (Kuma compatible): up or
// down, an optional message and latency. remote is the caller's address,
// used to judge whether missed pushes could be our own outage.
func (e *Engine) Push(name string, up bool, msg string, ping time.Duration, remote string) bool {
	found := false
	e.do(func() {
		m := e.monitors[name]
		rt := e.states[name]
		if m == nil || rt == nil || !m.Push || m.Paused {
			return
		}
		found = true
		now := e.now()
		rt.LastPush = now
		if a, err := parseAddr(remote); err == nil {
			rt.PushScope = netscope.OfAddr(a)
		}
		res := probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Detail: msg, Latency: ping}
		if !up {
			res = probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonAssertFailed, Detail: msg}
		}
		e.onResult(roundResult{name: name, defHash: m.DefHash, start: now, end: now, due: now, res: res, push: true})
	})
	return found
}

// checkPushDeadlines fails PUSH monitors whose push is overdue.
func (e *Engine) checkPushDeadlines(now time.Time) {
	for _, name := range e.sortedMonitorNames() {
		m := e.monitors[name]
		rt := e.states[name]
		if m == nil || rt == nil || !m.Push || m.Paused {
			continue
		}
		// A full period from the last push, or from when watching began
		// (this process, this definition, or resuming it), whichever is
		// later.
		last := rt.LastPush
		for _, t := range []time.Time{e.booted, rt.Since} {
			if t.After(last) {
				last = t
			}
		}
		deadline := last.Add(m.PushPeriod + m.PushGrace)
		if now.Before(deadline) || (!rt.LastCheck.IsZero() && !rt.LastCheck.Before(deadline) && now.Sub(rt.LastCheck) < m.PushPeriod) {
			continue
		}
		res := probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout, Detail: fmt.Sprintf("no push for %s", now.Sub(last).Round(time.Second))}
		e.onResult(roundResult{name: name, defHash: m.DefHash, start: now, end: now, due: now, res: res, push: true})
	}
}

// scheduleReminder computes the next daily open-incident reminder.
func (e *Engine) scheduleReminder(now time.Time) {
	h, mi := e.cfg.Tunables.TimeOfDay("notify.daily-reminder-at")
	e.nextReminder = NextDaily(now, h, mi, e.cfg.Location)
}

// dailyReminder sends one digest of still-open incidents per day.
func (e *Engine) dailyReminder(now time.Time) {
	if e.nextReminder.IsZero() || now.Before(e.nextReminder) {
		return
	}
	e.scheduleReminder(now)
	if !e.cfg.Tunables.Bool("notify.daily-reminder") {
		return
	}
	var lines []string
	var open []*Incident
	for _, inc := range e.incidents {
		if inc.State.Open() && !inc.State.Drill && inc.Kind != IncidentLow {
			open = append(open, inc)
		}
	}
	if len(open) == 0 {
		return
	}
	sort.Slice(open, func(i, j int) bool { return open[i].State.OpenedAt.Before(open[j].State.OpenedAt) })
	for _, inc := range open {
		lines = append(lines, e.text("reminder_line", inc.Title, now.Sub(inc.State.OpenedAt), inc.State.AckedBy, inc.State.NotifyCount))
	}
	e.enqueue(&Intent{Kind: "DIGEST", Policy: e.policy("system").Name, CreatedAt: now, DueAt: now, System: true,
		Message: notify.Message{Kind: "DIGEST", Priority: "high", Title: e.text("reminder_title", len(open)), Body: strings.Join(lines, "\n"), Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang}})
}
