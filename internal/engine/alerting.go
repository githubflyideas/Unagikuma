package engine

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// inPeriod reports whether a channel may be used now: always, unless it
// is limited to a time period (or to outside one).
func (e *Engine) inPeriod(ch ChannelSpec, now time.Time) bool {
	if ch.Period == nil {
		return true
	}
	return ch.Period.Contains(now, e.location()) != ch.Outside
}

func (e *Engine) location() *time.Location {
	if e.cfg.Location != nil {
		return e.cfg.Location
	}
	return time.Local
}

// groupMembers returns the members of a contact group usable now.
func (e *Engine) groupMembers(ch ChannelSpec, now time.Time) []ChannelSpec {
	var out []ChannelSpec
	for _, name := range ch.Members {
		if m, ok := e.cfg.Channels[name]; ok && e.inPeriod(m, now) {
			out = append(out, m)
		}
	}
	return out
}

// sendGroup notifies every member at once; the group succeeds when any
// member does. Runs outside the engine loop.
func (e *Engine) sendGroup(ctx context.Context, members []ChannelSpec, msg *notify.Message) (notify.Attempt, []notify.Attempt) {
	if len(members) == 0 {
		return notify.Attempt{Error: "no member is inside its time period"}, nil
	}
	atts := make([]notify.Attempt, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m2 := *msg
			atts[i] = e.deps.Send(ctx, m.Channel, &m2)
			atts[i].Channel = m.Name
		}()
	}
	wg.Wait()
	sum := notify.Attempt{}
	var errs []string
	for _, a := range atts {
		sum.Latency = max(sum.Latency, a.Latency)
		if a.OK {
			sum.OK = true
		} else {
			errs = append(errs, a.Channel+": "+a.Error)
		}
	}
	if !sum.OK {
		sum.Error = strings.Join(errs, "; ")
	}
	return sum, atts
}

// escalate sends the policy's escalation steps of open, unacknowledged
// incidents once they are old enough — one step per incident per tick.
func (e *Engine) escalate(now time.Time) {
	for _, inc := range e.incidents {
		if (inc.Kind != IncidentNormal && inc.Kind != IncidentSystem) || !inc.State.Open() || inc.State.Drill ||
			inc.State.Acked || now.Before(inc.State.SilencedUntil) {
			continue
		}
		pol := e.policy(inc.Policy)
		if inc.State.Escalated >= len(pol.Escalate) {
			continue
		}
		step := pol.Escalate[inc.State.Escalated]
		if now.Sub(inc.State.OpenedAt) < step.After || e.maintenanceIncident(inc) || e.pausedIncident(inc) {
			continue
		}
		inc.State.Escalated++
		e.saveIncident(inc)
		msg := e.message(inc, PlannedNotice{Kind: NoticeEscalate, Seq: inc.State.NotifyCount}, now)
		e.enqueue(&Intent{IncidentID: inc.ID, Kind: string(NoticeEscalate), Policy: inc.Policy, Message: msg,
			CreatedAt: now, DueAt: now, Chain: step.Channels, System: inc.Kind == IncidentSystem})
		e.event(Event{Type: EventIncident, Severity: "warning", Monitor: inc.Monitor,
			Message: "escalated to " + strings.Join(step.Channels, ", "),
			Data:    map[string]any{"msg": "escalated", "channels": strings.Join(step.Channels, ", "), "incident_id": inc.ID, "step": inc.State.Escalated}})
	}
}

// nagiosState names a state the way Nagios event handlers expect.
func nagiosState(s string) string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusDegraded:
		return "WARNING"
	case StatusCritical:
		return "CRITICAL"
	}
	return "UNKNOWN"
}

// runHandler starts the monitor's event handler for a state change, in
// the background and at most one at a time per monitor. Nagios macros in
// its arguments are substituted and the same values are in the
// environment.
func (e *Engine) runHandler(m *MonitorSpec, t Transition, rt *monRuntime) {
	if e.runCtx == nil || e.handling[m.Name] {
		return
	}
	state, stype := nagiosState(t.To.Display()), "SOFT"
	if t.To.Hard {
		stype = "HARD"
	}
	attempt := strconv.Itoa(max(t.To.Attempt, 1))
	addr := m.Probe.Opts["address"]
	if addr == "" {
		addr = targetHost(m.Probe)
	}
	cmdline := strings.NewReplacer("$SERVICESTATE$", state, "$SERVICESTATETYPE$", stype, "$SERVICEATTEMPT$", attempt,
		"$HOSTADDRESS$", addr, "$SERVICEDESC$", m.Name).Replace(m.Handler)
	env := []string{"UNAGIKUMA_MONITOR=" + m.Name, "UNAGIKUMA_STATE=" + state, "UNAGIKUMA_STATE_TYPE=" + stype,
		"UNAGIKUMA_ATTEMPT=" + attempt, "UNAGIKUMA_ADDRESS=" + addr, "UNAGIKUMA_REASON=" + rt.LastReason}
	timeout := e.cfg.Tunables.Duration("engine.handler-timeout")
	e.handling[m.Name] = true
	name := m.Name
	go func() {
		ctx, cancel := context.WithTimeout(e.runCtx, timeout)
		code, out, err := probe.RunHandler(ctx, cmdline, env)
		cancel()
		e.do(func() {
			delete(e.handling, name)
			sev, text := "info", "event handler exited "+strconv.Itoa(code)
			if err != nil {
				sev, text = "warning", "event handler failed: "+err.Error()
			} else if code != 0 {
				sev = "warning"
			}
			e.event(Event{Type: EventSystem, Severity: sev, Monitor: name, Message: text,
				Data: map[string]any{"msg": "handler_ran", "code": code, "output": out, "state": state, "state_type": stype}})
		})
	}()
}
