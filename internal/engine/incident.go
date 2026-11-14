package engine

import (
	"context"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
)

// policy resolves a policy name: undefined "system" inherits "default";
// an undefined "default" has an empty chain (the UI shows the "alerts not
// configured" banner).
func (e *Engine) policy(name string) PolicySpec {
	if name == "" {
		name = "default"
	}
	if p, ok := e.cfg.Policies[name]; ok {
		return p
	}
	if name == "system" {
		if p, ok := e.cfg.Policies["default"]; ok {
			p.Name = "system"
			return p
		}
	}
	return PolicySpec{Name: name, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(e.cfg.Tunables)}
}

// monitorFailed handles a monitor entering HARD CRITICAL.
func (e *Engine) monitorFailed(m *MonitorSpec, rt *monRuntime, pol PolicySpec, now time.Time) {
	prev := e.byMonitor[m.Name]
	var prevState *IncidentState
	if prev != nil {
		prevState = &prev.State
	}
	st, notices, events := Fail(prevState, now, reasonClass(rt.LastReason), pol.Notify)
	inc := prev
	if prev == nil || st != &prev.State {
		inc = &Incident{Monitor: m.Name, Kind: IncidentNormal, Policy: pol.Name, State: *st, Title: m.Title}
		if prev != nil {
			e.retireIncident(prev)
		}
	}
	inc.Reason, inc.Detail = rt.LastReason, rt.LastDetail
	inc.openClass = reasonClass(rt.LastReason)
	e.saveIncident(inc)
	e.byMonitor[m.Name] = inc
	e.incidents[inc.ID] = inc
	for _, ev := range events {
		e.incidentEvent(inc, ev)
	}
	if prev == inc {
		e.incidentEvent(inc, "")
	} else {
		e.event(Event{Type: EventIncident, Severity: "critical", Monitor: m.Name, Message: "incident opened", Data: map[string]any{"msg": "incident_opened", "incident_id": inc.ID, "reason": inc.Reason}})
	}
	e.notices(inc, notices, now)
}

// monitorRecovered handles a HARD CRITICAL monitor recovering.
func (e *Engine) monitorRecovered(m *MonitorSpec, rt *monRuntime, pol PolicySpec, now time.Time) {
	inc := e.byMonitor[m.Name]
	if inc == nil || !inc.State.Open() {
		return
	}
	notices := Recover(&inc.State, now, pol.Notify)
	e.expireNotices(inc, now)
	e.saveIncident(inc)
	e.event(Event{Type: EventIncident, Severity: "info", Monitor: m.Name, Message: "incident resolved",
		Data: map[string]any{"msg": "incident_resolved", "incident_id": inc.ID, "duration_s": int(now.Sub(inc.State.OpenedAt).Seconds()), "notified": inc.State.NotifyCount}})
	e.notices(inc, notices, now)
}

// expireNotices drops an incident's problem notices still waiting to be
// sent: once it recovered they would announce a problem that is over.
func (e *Engine) expireNotices(inc *Incident, now time.Time) {
	for id, in := range e.intents {
		if in.IncidentID != inc.ID || NoticeKind(in.Kind) == NoticeRecovery || in.Delivery.Pos() > 0 || in.Delivery.Steps[0].Attempts > 0 {
			continue
		}
		in.State, in.UpdatedAt = IntentDropped, now
		_ = e.deps.Sink.SaveIntent(in)
		delete(e.intents, id)
	}
}

// openLow opens the low-priority incident of a DEGRADED monitor.
func (e *Engine) openLow(m *MonitorSpec, rt *monRuntime, pol PolicySpec, now time.Time) {
	if inc := e.lowByMon[m.Name]; inc != nil && inc.State.Open() {
		return
	}
	inc := &Incident{Monitor: m.Name, Kind: IncidentLow, Policy: pol.Name, Title: m.Title, Reason: "slow",
		State: IncidentState{Low: true, OpenedAt: now, HardAt: now, Tracked: true}}
	notices, _ := Advance(&inc.State, now, pol.Notify)
	e.saveIncident(inc)
	e.lowByMon[m.Name] = inc
	e.incidents[inc.ID] = inc
	e.notices(inc, notices, now)
}

// hasOpenIncident reports whether the monitor has an incident of any kind.
func (e *Engine) hasOpenIncident(name string) bool {
	return e.byMonitor[name] != nil || e.lowByMon[name] != nil || e.regional[name] != nil
}

// closeMonitorIncidents ends a monitor's incidents without notification
// (monitor deleted or redefined).
func (e *Engine) closeMonitorIncidents(name, why string) {
	now := e.now()
	for _, mm := range []map[string]*Incident{e.byMonitor, e.lowByMon, e.regional} {
		inc := mm[name]
		if inc == nil {
			continue
		}
		if inc.State.ResolvedAt.IsZero() {
			inc.State.ResolvedAt = now
		}
		inc.State.Closed = true
		inc.Detail = why
		e.saveIncident(inc)
		e.retireIncident(inc)
		delete(mm, name)
		e.event(Event{Type: EventIncident, Severity: "info", Monitor: name, Message: "incident closed: monitor " + why, Data: map[string]any{"msg": "closed_" + why, "incident_id": inc.ID}})
	}
}

// retireIncident forgets an incident and drops its undelivered notices.
func (e *Engine) retireIncident(inc *Incident) {
	delete(e.incidents, inc.ID)
	for id, in := range e.intents {
		if in.IncidentID == inc.ID && in.Kind != string(NoticeRecovery) {
			in.State, in.UpdatedAt = IntentMerged, e.now()
			_ = e.deps.Sink.SaveIntent(in)
			delete(e.intents, id)
		}
	}
}

// advanceIncidents applies time to every incident: delayed first notices,
// repeats, ACK and silence expiry, summaries, closing.
func (e *Engine) advanceIncidents(now time.Time) {
	ids := make([]int64, 0, len(e.incidents))
	for id := range e.incidents {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		inc := e.incidents[id]
		if inc == nil {
			continue
		}
		// Repeats pause while the monitor is held by UNKNOWN (our own
		// outage): telling people again would be noise.
		if inc.Kind == IncidentNormal && inc.State.Open() {
			if rt := e.states[inc.Monitor]; rt != nil && rt.State.Held {
				continue
			}
		}
		// Repeats wait while the monitor is in maintenance.
		if inc.State.Open() && e.maintenanceIncident(inc) {
			continue
		}
		pol := e.policy(inc.Policy)
		before := inc.State
		notices, events := Advance(&inc.State, now, e.effectivePolicy(inc, pol))
		if len(notices) == 0 && len(events) == 0 && before == inc.State {
			continue
		}
		for _, ev := range events {
			e.incidentEvent(inc, ev)
		}
		e.saveIncident(inc)
		e.notices(inc, notices, now)
		if inc.State.Closed {
			e.retireClosed(inc)
		}
	}
}

// effectivePolicy adjusts the notification discipline for system incidents:
// a local outage shorter than localnet.notify-after is never notified.
func (e *Engine) effectivePolicy(inc *Incident, pol PolicySpec) NotifyPolicy {
	p := pol.Notify
	if inc.Kind == IncidentSystem && inc.System == SystemLocalNetwork {
		p.Delay = max(p.Delay, e.cfg.Tunables.Duration("localnet.notify-after"))
	}
	return p
}

func (e *Engine) retireClosed(inc *Incident) {
	delete(e.incidents, inc.ID)
	for _, mm := range []map[string]*Incident{e.byMonitor, e.lowByMon, e.system} {
		for k, v := range mm {
			if v == inc {
				delete(mm, k)
			}
		}
	}
}

func (e *Engine) incidentEvent(inc *Incident, ev string) {
	switch ev {
	case "":
		return
	case IncidentCapped:
		e.saved(CounterCapSaved, inc.Monitor, 1)
	}
	e.event(Event{Type: EventIncident, Severity: "info", Monitor: inc.Monitor, Message: "incident " + strings.ReplaceAll(ev, "_", " "),
		Data: map[string]any{"msg": "incident_" + ev, "incident_id": inc.ID, "event": ev, "count": inc.State.NotifyCount}})
}

// notices turns planned notices into durable intents.
func (e *Engine) notices(inc *Incident, ns []PlannedNotice, now time.Time) {
	if len(ns) == 0 {
		return
	}
	class, evidence := e.classOf(inc)
	if e.policy(inc.Policy).Mute && inc.Kind != IncidentSystem {
		e.event(Event{Type: EventNotify, Severity: "info", Monitor: inc.Monitor, Message: "not notified: muted", Data: map[string]any{"msg": "not_notified_muted"}})
		for _, n := range ns {
			e.decide(Decision{IncidentID: inc.ID, Monitor: inc.Monitor, Kind: string(n.Kind), Step: LedSuppressed, Class: class,
				Evidence: evidence, DetectedAt: inc.State.HardAt, Note: "muted"})
		}
		return
	}
	if e.maintenanceIncident(inc) {
		e.event(Event{Type: EventNotify, Severity: "info", Monitor: inc.Monitor, Message: "not notified: maintenance window", Data: map[string]any{"msg": "not_notified_maint"}})
		for _, n := range ns {
			e.decide(Decision{IncidentID: inc.ID, Monitor: inc.Monitor, Kind: string(n.Kind), Step: LedSuppressed, Class: class,
				Evidence: evidence, DetectedAt: inc.State.HardAt, Note: "maintenance window"})
		}
		return
	}
	for _, n := range ns {
		msg := e.message(inc, n, now)
		msg.Class = class
		if line := e.classLine(class, n.Kind, evidence); line != "" {
			msg.Body = strings.TrimSpace(msg.Body + "\n" + line)
		}
		in := &Intent{
			IncidentID: inc.ID, Seq: n.Seq, Kind: string(n.Kind), Policy: inc.Policy,
			Message: msg, CreatedAt: now, DueAt: now, System: inc.Kind == IncidentSystem, Evidence: evidence,
		}
		if e.enqueue(in) {
			e.decideIntent(in, LedQueued, "")
		}
		// Whoever an escalation told about the problem hears that it is
		// over too (team managers among them).
		if n.Kind == NoticeRecovery && inc.State.Escalated > 0 {
			if chans := e.escalatedChannels(inc); len(chans) > 0 {
				extra := *in
				extra.Chain, extra.Delivery = chans, Delivery{}
				if e.enqueue(&extra) {
					e.decideIntent(&extra, LedQueued, "")
				}
			}
		}
	}
}

// escalatedChannels are the channels of the escalation steps an incident
// already went through.
func (e *Engine) escalatedChannels(inc *Incident) []string {
	pol := e.policy(inc.Policy)
	seen := map[string]bool{}
	var out []string
	for i := 0; i < inc.State.Escalated && i < len(pol.Escalate); i++ {
		for _, c := range pol.Escalate[i].Channels {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// enqueue persists a new intent and queues it for delivery.
func (e *Engine) enqueue(in *Intent) bool {
	pol := e.policy(in.Policy)
	in.ID = newID()
	in.Message.DeliveryID = in.ID
	chain, mode := pol.Chain, pol.Mode
	if len(in.Chain) > 0 {
		chain, mode = in.Chain, DeliverAll
	}
	in.Delivery = NewDelivery(chain, mode)
	in.State = IntentPending
	in.UpdatedAt = in.CreatedAt
	if err := e.deps.Sink.SaveIntent(in); err != nil {
		e.log.Error("cannot persist notification intent", "err", err)
	}
	if len(in.Delivery.Steps) == 0 {
		in.State = IntentFailed
		_ = e.deps.Sink.SaveIntent(in)
		e.event(Event{Type: EventNotify, Severity: "warning", Monitor: in.Message.Monitor, Message: "not notified: policy " + pol.Name + " has no channels", Data: map[string]any{"msg": "no_channels", "policy": pol.Name}})
		e.decideIntent(in, LedFailed, "policy "+pol.Name+" has no channels")
		return false
	}
	e.intents[in.ID] = in
	return true
}

// message builds the notification for an incident notice.
func (e *Engine) message(inc *Incident, n PlannedNotice, now time.Time) notify.Message {
	msg := notify.Message{
		Kind: string(n.Kind), Priority: "high", Monitor: inc.Monitor, Title: inc.Title,
		GlobalIncidentID: inc.GlobalID, Reason: inc.Reason, Detail: inc.Detail, Count: n.Seq, Max: n.Max, IncidentID: inc.ID,
		Duration: now.Sub(inc.State.OpenedAt), Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang,
	}
	if inc.Kind == IncidentLow || inc.Kind == IncidentRegional {
		msg.Priority = "low"
	}
	if m := e.monitors[inc.Monitor]; m != nil {
		if msg.Title == "" {
			msg.Title = m.Title
		}
		msg.Type, msg.Target, msg.Depth, msg.Tags = m.Probe.Type, m.Probe.Target, m.Depth, m.Tags
		if rt := e.states[m.Name]; rt != nil {
			msg.Status = rt.State.Display()
			msg.Observations = e.siteObservationSnapshot(m.Name)
			if len(e.cfg.Nodes) > 0 {
				msg.Site = e.cfg.SourceNode
			}
		}
		if e.cfg.BaseURL != "" {
			msg.DetailURL = strings.TrimRight(e.cfg.BaseURL, "/") + "/#/monitors/" + url.PathEscape(m.Name)
		}
	}
	if msg.Title == "" {
		msg.Title = inc.Monitor
	}
	if n.Kind == NoticeRecovery {
		msg.Status = StatusOK
		if n.Flaps > 0 {
			msg.Body = e.text("recovered_after_flaps", n.Flaps)
		}
	}
	if e.localnet.Released() && inc.Kind != IncidentSystem && n.Kind != NoticeRecovery {
		if act := e.localnet.Active(); len(act) > 0 {
			msg.Body = strings.TrimSpace(msg.Body + "\n" + e.text("hold_note", act[0].Since))
		}
	}
	if inc.Kind == IncidentSystem {
		msg.Kind = "SYSTEM"
		msg.Body = inc.Detail
		if n.Kind == NoticeRecovery {
			// Its own title: "node bj is back", not "node bj is offline".
			msg.Title, msg.Body = e.resolvedTitle(inc), inc.Summary
		}
	}
	return msg
}

// resolvedTitle is the title of a system incident's recovery notice.
func (e *Engine) resolvedTitle(inc *Incident) string {
	if node, ok := strings.CutPrefix(inc.System, SystemNodeOffline+":"); ok {
		return e.text("node_back", e.nodeLabel(node))
	}
	switch inc.System {
	case SystemLocalNetwork, SystemOverload, SystemAllFailed, SystemStorage:
		return e.text("system_resolved_" + inc.System)
	}
	return e.text("system_resolved", inc.Title)
}

func (e *Engine) saveIncident(inc *Incident) {
	if inc.SourceNode == "" {
		inc.SourceNode = e.cfg.SourceNode
	}
	// An incident the store could not take yet (a full disk) lives under
	// a temporary negative id, so its alerts still credit it and its
	// recovery is still owed; the first save that works gives it its id.
	temp := inc.ID
	if temp < 0 {
		inc.ID = 0
	}
	if err := e.deps.Sink.SaveIncident(inc); err != nil {
		e.log.Error("cannot persist incident", "err", err, "monitor", inc.Monitor)
	}
	switch {
	case inc.ID == 0 && temp < 0:
		inc.ID = temp
	case inc.ID == 0:
		e.tempIncident--
		inc.ID = e.tempIncident
	case temp < 0:
		delete(e.incidents, temp)
		for _, in := range e.intents {
			if in.IncidentID == temp {
				in.IncidentID = inc.ID
				in.Message.IncidentID = inc.ID
			}
			for i, c := range in.Covers {
				if c == temp {
					in.Covers[i] = inc.ID
				}
			}
		}
	}
	e.incidents[inc.ID] = inc
}

func (e *Engine) saveState(name string, rt *monRuntime) {
	rec := StateRecord{Name: name, State: rt.State, Status: e.statusOf(e.monitors[name], rt), Reason: rt.LastReason, Detail: rt.LastDetail,
		LatencyMS: ms(rt.LastResult.Latency), LastCheck: rt.LastCheck, LastChange: rt.LastChange, DefHash: rt.DefHash}
	if inc := e.byMonitor[name]; inc != nil && inc.State.Open() {
		rec.IncidentID = inc.ID
	}
	e.deps.Sink.SaveState(rec)
}

func (e *Engine) event(ev Event) {
	if ev.At.IsZero() {
		ev.At = e.now()
	}
	e.deps.Sink.Event(ev)
	// The same story in the log, one structured line per event, for
	// whoever reads journald or ships logs elsewhere.
	lvl := slog.LevelInfo
	if ev.Severity == "warning" || ev.Severity == "critical" {
		lvl = slog.LevelWarn
	}
	attrs := []any{"event", ev.Type, "severity", ev.Severity}
	if ev.Monitor != "" {
		attrs = append(attrs, "monitor", ev.Monitor)
	}
	for _, k := range []string{"msg", "incident_id", "channel", "by"} {
		if v, ok := ev.Data[k]; ok {
			key := k
			if k == "msg" {
				key = "code"
			}
			attrs = append(attrs, key, v)
		}
	}
	e.log.Log(context.Background(), lvl, ev.Message, attrs...)
}

// supersedeLow closes a monitor's low-priority incident without a
// notification: a failure incident replaces it.
func (e *Engine) supersedeLow(name string, inc *Incident, now time.Time) {
	if inc.State.ResolvedAt.IsZero() {
		inc.State.ResolvedAt = now
	}
	inc.State.Closed = true
	inc.Detail = "superseded by a failure"
	e.saveIncident(inc)
	e.retireIncident(inc)
	delete(e.lowByMon, name)
	e.event(Event{Type: EventIncident, Severity: "info", Monitor: name, Message: "degraded incident closed: the monitor failed", Data: map[string]any{"msg": "degraded_superseded", "incident_id": inc.ID}})
}
