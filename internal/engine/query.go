package engine

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// Errors returned by commands.
var (
	ErrNotFound    = errors.New("not found")
	ErrRateLimited = errors.New("rate limited")
	ErrStopped     = errors.New("engine stopped")
)

// MonitorRow is the live view of one monitor.
type MonitorRow struct {
	Name          string
	Title         string
	Group         string
	Tags          []string
	Type          string
	Target        string
	Depth         string
	Status        string // display status (paused overrides)
	Hard          bool
	Attempt       int
	Attempts      int
	Partial       bool
	Held          bool
	Degraded      bool
	UnknownReason string
	Reason        string
	Detail        string
	LatencyMS     float64
	LastCheck     time.Time
	LastChange    time.Time
	NextCheck     time.Time
	IncidentID    int64
	Acked         bool
	Silenced      bool
	Paused        bool
	Scope         string
	Metrics       map[string]float64
	// Cluster monitors: the last round's votes and how they were counted.
	Votes  []VoteView
	Quorum *Verdict
	// Regional: a lasting partial failure opened a low-priority incident.
	RegionalID int64
	// Parents this monitor depends on; Unreachable when one of them is down.
	Parents     []string
	Unreachable bool
	Maintenance string // the open window covering this monitor
}

func (e *Engine) row(name string, now time.Time) (MonitorRow, bool) {
	m, rt := e.monitors[name], e.states[name]
	if m == nil || rt == nil {
		return MonitorRow{}, false
	}
	r := MonitorRow{
		Name: m.Name, Title: m.Title, Group: m.Group, Tags: m.Tags, Type: m.Probe.Type, Target: m.Probe.Target, Depth: m.Depth,
		Status: e.statusOf(m, rt), Hard: rt.State.Hard, Attempt: rt.State.Attempt, Attempts: m.Attempts,
		Partial: rt.State.Partial, Held: rt.State.Held, Degraded: rt.State.Degraded, UnknownReason: rt.State.UnknownReason,
		Reason: rt.LastReason, Detail: rt.LastDetail, LatencyMS: ms(rt.LastResult.Latency),
		LastCheck: rt.LastCheck, LastChange: rt.LastChange, Paused: m.Paused, Scope: string(rt.Scope), Metrics: rt.LastResult.Metrics,
		Parents: m.Parents, Unreachable: rt.Unreachable,
	}
	if m.Push {
		r.Type = "PUSH"
	}
	if e.inMaint(name) {
		r.Maintenance = e.maintOf[name]
	}
	if m.Paused {
		r.Status = StatusPaused
	}
	if due, ok := e.sched.Due(name); ok {
		r.NextCheck = due
	}
	if len(rt.LastVotes) > 0 {
		r.Votes = rt.LastVotes
		v := rt.LastVerdict
		r.Quorum = &v
	}
	if inc := e.regional[name]; inc != nil && inc.State.Open() {
		r.RegionalID = inc.ID
	}
	if inc := e.byMonitor[name]; inc != nil && inc.State.Open() {
		r.IncidentID = inc.ID
		r.Acked = inc.State.Acked
		r.Silenced = now.Before(inc.State.SilencedUntil)
	}
	return r, true
}

// Rows returns every monitor's live row, sorted by name.
func (e *Engine) Rows() []MonitorRow {
	var out []MonitorRow
	e.do(func() {
		now := e.now()
		for _, n := range e.sortedMonitorNames() {
			if r, ok := e.row(n, now); ok {
				out = append(out, r)
			}
		}
	})
	return out
}

// Row returns one monitor's live row.
func (e *Engine) Row(name string) (MonitorRow, bool) {
	var r MonitorRow
	var ok bool
	e.do(func() { r, ok = e.row(name, e.now()) })
	return r, ok
}

// Overview is the data behind the overview page counters.
type Overview struct {
	Serial           uint32
	Counts           map[string]int
	Unhandled        int
	Degraded         int
	Health           Health
	Anomalies        []Anomaly
	LastInterruption *Interruption
	OpenSystem       []string
	PendingIntents   int
}

// Health is the state of this host's own network path: gateway, uplink,
// DNS and the internet exit.
func (e *Engine) Health() Health {
	var h Health
	e.do(func() { h = e.health(e.now()) })
	return h
}

func (e *Engine) health(now time.Time) Health {
	h := e.localnet.Health(now)
	h.Exit, h.ExitOK, h.ExitRTT, h.ExitAt, h.ExitIdle = e.exit.Addr, e.exit.OK, e.exit.RTT, e.exit.At, e.exit.Idle
	return h
}

// Overview computes the counters with one definition shared by the bell,
// sidebar, tab title and overview cards.
func (e *Engine) Overview() Overview {
	var o Overview
	e.do(func() {
		now := e.now()
		o.Serial = e.cfg.Serial
		o.Counts = map[string]int{}
		for _, n := range e.sortedMonitorNames() {
			r, _ := e.row(n, now)
			o.Counts[r.Status]++
			if r.Status == StatusDegraded {
				o.Degraded++
			}
		}
		for _, inc := range e.incidents {
			if Unhandled(inc, now) {
				o.Unhandled++
			}
			if inc.Kind == IncidentSystem && inc.State.Open() {
				o.OpenSystem = append(o.OpenSystem, inc.System)
			}
		}
		sort.Strings(o.OpenSystem)
		o.Health = e.health(now)
		o.Anomalies = e.localnet.Active()
		if !e.lastInterruption.End.IsZero() {
			in := e.lastInterruption
			o.LastInterruption = &in
		}
		o.PendingIntents = len(e.intents)
	})
	return o
}

// OpenIncidents returns the incidents the engine is tracking (open, or
// resolved but still reopenable).
// Unhandled is the one definition of an unhandled problem, shared by the
// sidebar, the overview, the problems page and /metrics: an open incident
// of any kind (regional and low-priority ones included), not a drill,
// neither acknowledged nor silenced. A monitor that cannot be judged
// (UNKNOWN) has no incident and is counted under its status instead.
func Unhandled(inc *Incident, now time.Time) bool {
	return inc.State.Open() && !inc.State.Drill && !Handled(inc, now)
}

// Handled reports whether someone took an incident on.
func Handled(inc *Incident, now time.Time) bool {
	return inc.State.Acked || now.Before(inc.State.SilencedUntil)
}

func (e *Engine) OpenIncidents() []Incident {
	var out []Incident
	e.do(func() {
		for _, inc := range e.incidents {
			out = append(out, *inc)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (e *Engine) withIncident(id int64, f func(*Incident, PolicySpec, time.Time)) error {
	err := ErrNotFound
	if !e.do(func() {
		inc := e.incidents[id]
		if inc == nil {
			return
		}
		err = nil
		f(inc, e.policy(inc.Policy), e.now())
		e.saveIncident(inc)
		if inc.Kind == IncidentNormal {
			if rt := e.states[inc.Monitor]; rt != nil {
				e.saveState(inc.Monitor, rt)
			}
		}
	}) {
		return ErrStopped
	}
	return err
}

// Ack acknowledges an incident (expire 0 = the policy's ack-expire).
func (e *Engine) Ack(id int64, by, comment string, expire time.Duration) error {
	return e.withIncident(id, func(inc *Incident, pol PolicySpec, now time.Time) {
		Ack(&inc.State, by, now, expire, pol.Notify)
		inc.AckComment = comment
		e.decide(Decision{At: now, IncidentID: id, Monitor: inc.Monitor, Step: LedAcked, Note: by})
		e.event(Event{Type: EventAck, Severity: "info", Monitor: inc.Monitor, Message: "acknowledged by " + by, Data: map[string]any{"msg": "acked", "by": by, "incident_id": id, "comment": comment}})
	})
}

// Unack withdraws an acknowledgement.
func (e *Engine) Unack(id int64, by string) error {
	return e.withIncident(id, func(inc *Incident, _ PolicySpec, _ time.Time) {
		Unack(&inc.State)
		inc.AckComment = ""
		e.event(Event{Type: EventAck, Severity: "info", Monitor: inc.Monitor, Message: "acknowledgement withdrawn by " + by, Data: map[string]any{"msg": "unacked", "by": by, "incident_id": id}})
	})
}

// Silence suppresses an incident's notifications until the given time.
func (e *Engine) Silence(id int64, by string, until time.Time) error {
	return e.withIncident(id, func(inc *Incident, _ PolicySpec, _ time.Time) {
		Silence(&inc.State, until)
		e.event(Event{Type: EventSilence, Severity: "info", Monitor: inc.Monitor, Message: "silenced by " + by, Data: map[string]any{"msg": "silenced", "by": by, "incident_id": id, "until": until}})
	})
}

// CheckNow runs a monitor's next round right away (rate limited per
// monitor by probe.check-now-rate).
func (e *Engine) CheckNow(name string) error {
	err := ErrNotFound
	if !e.do(func() {
		m, rt := e.monitors[name], e.states[name]
		if m == nil || rt == nil || m.Push {
			return
		}
		now := e.now()
		if gap := e.cfg.Tunables.Duration("probe.check-now-rate"); !rt.CheckNowAt.IsZero() && now.Sub(rt.CheckNowAt) < gap {
			err = ErrRateLimited
			return
		}
		rt.CheckNowAt = now
		err = nil
		if !rt.Running {
			e.schedule(name, now)
		}
	}) {
		return ErrStopped
	}
	return err
}

// TestPolicy sends a drill message through a policy's chain, optionally
// forcing the first failFirst channels to fail to prove the fallback works.
func (e *Engine) TestPolicy(policy string, failFirst int, by string) (string, error) {
	var id string
	if !e.do(func() {
		now := e.now()
		in := &Intent{Kind: "DRILL", Policy: policy, CreatedAt: now, DueAt: now, ForceFail: failFirst,
			Message: notify.Message{Kind: "DRILL", Priority: "low", Title: "unagikuma drill", DryRun: true, Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang,
				Body: "chain test requested by " + by}}
		e.enqueue(in)
		id = in.ID
	}) {
		return "", ErrStopped
	}
	return id, nil
}

// TestChannel delivers a test message to one channel synchronously.
func (e *Engine) TestChannel(ctx context.Context, ch notify.Channel) notify.Attempt {
	msg := notify.Message{Kind: "TEST", Priority: "low", Title: "unagikuma test", Time: e.now(), Instance: e.cfg.Instance, Lang: e.cfg.Lang, DeliveryID: newID()}
	if ch.Kind == "group" {
		// A test reaches every member, whatever their time periods.
		var members []ChannelSpec
		e.do(func() {
			names := strings.Split(ch.Opts["members"], ",")
			if c, ok := e.cfg.Channels[ch.Name]; ok && ch.Opts["members"] == "" {
				names = c.Members
			}
			for _, n := range names {
				if m, ok := e.cfg.Channels[strings.TrimSpace(n)]; ok {
					members = append(members, m)
				}
			}
		})
		att, _ := e.sendGroup(ctx, members, &msg)
		att.Channel = ch.Name
		return att
	}
	att := e.deps.Send(ctx, ch, &msg)
	att.Channel = ch.Name
	return att
}

// ProbeOnce runs a check outside any round ("test now" in the editor).
func (e *Engine) ProbeOnce(ctx context.Context, s probe.Spec) probe.Result {
	return e.deps.Probe(ctx, s)
}

// PredictStep is one predicted notification of a simulated outage.
type PredictStep struct {
	At      time.Duration // since the outage started
	Kind    NoticeKind
	Seq     int
	Max     int
	Channel string // first channel of the chain
}

// Predict simulates a monitor failing for the given duration and then
// recovering, using its real attempts, intervals and policy, and returns
// the notifications that would be sent (the "predict only" drill).
func (e *Engine) Predict(name string, outage time.Duration) ([]PredictStep, error) {
	var out []PredictStep
	err := ErrNotFound
	e.do(func() {
		m := e.monitors[name]
		if m == nil {
			return
		}
		err = nil
		pol := e.policy(m.Policy)
		first := ""
		if len(pol.Chain) > 0 {
			first = pol.Chain[0]
		}
		t0 := time.Time{}.Add(time.Hour)
		st := MonState{Avail: StatusOK, Hard: true}
		var inc *IncidentState
		add := func(at time.Time, ns []PlannedNotice) {
			for _, n := range ns {
				out = append(out, PredictStep{At: at.Sub(t0), Kind: n.Kind, Seq: n.Seq, Max: n.Max, Channel: first})
			}
		}
		at := t0
		step := e.cfg.Tunables.Duration("suspend.tick")
		nextRound := t0
		for at.Before(t0.Add(outage)) {
			if !at.Before(nextRound) {
				tr := Step(st, Verdict{Kind: VerdictCritical, Fail: 1, N: 1, Healthy: 1, Q: 1, QEff: 1}, StepParams{Attempts: m.Attempts, RecoverAttempts: m.RecoverAttempts})
				st = tr.To
				if tr.EnterHard {
					var ns []PlannedNotice
					inc, ns, _ = Fail(inc, at, "network", pol.Notify)
					add(at, ns)
				}
				iv := m.Interval
				if tr.SoftFailure && m.Retry > 0 {
					iv = m.Retry
				}
				nextRound = at.Add(iv)
			}
			if inc != nil {
				ns, _ := Advance(inc, at, pol.Notify)
				add(at, ns)
			}
			at = at.Add(max(step, time.Second))
		}
		if inc != nil {
			add(at, Recover(inc, at, pol.Notify))
		}
	})
	return out, err
}

// statusOf is the status shown for a monitor everywhere (list, saved
// state, events): pausing and maintenance override what the checks say,
// and a failure explained by a down parent shows as UNREACHABLE.
func (e *Engine) statusOf(m *MonitorSpec, rt *monRuntime) string {
	switch {
	case m != nil && m.Paused:
		return StatusPaused
	case m != nil && e.inMaint(m.Name):
		return StatusMaintenance
	case rt.Unreachable && rt.State.Avail == StatusCritical:
		return StatusUnreachable
	}
	return rt.State.Display()
}
