package engine

import (
	"time"

	"github.com/githubflyideas/unagikuma/internal/schedule"
)

// MaintSpec is a maintenance window over some monitors. While it is open
// their failures open no incident, open incidents send nothing, and the
// time does not count against uptime. A monitor still failing when the
// window closes is notified then.
type MaintSpec struct {
	Name     string
	Title    string
	Monitors []string
	Schedule schedule.Spec
	Notify   bool // announce start and end through the system policy
}

// MaintView is an open or upcoming window, for the UI.
type MaintView struct {
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Monitors []string  `json:"monitors"`
	Open     bool      `json:"open"`
	Start    time.Time `json:"start,omitempty"`
	End      time.Time `json:"end,omitempty"`
	Next     time.Time `json:"next,omitempty"`
	NextEnd  time.Time `json:"next_end,omitzero"`
}

func (e *Engine) inMaint(monitor string) bool {
	_, ok := e.maintOf[monitor]
	return ok
}

// checkMaint opens and closes windows. It runs on every tick and after a
// configuration change.
func (e *Engine) checkMaint(now time.Time) {
	loc := e.cfg.Location
	if loc == nil {
		loc = time.Local
	}
	active := map[string]string{}
	open := map[string]time.Time{}
	for _, mt := range e.cfg.Maints {
		_, end, ok := mt.Schedule.Active(now, loc)
		if !ok {
			continue
		}
		open[mt.Name] = end
		for _, m := range mt.Monitors {
			if _, dup := active[m]; !dup {
				active[m] = mt.Name
			}
		}
	}
	for _, mt := range e.cfg.Maints {
		_, isOpen := open[mt.Name]
		_, wasOpen := e.maintOpen[mt.Name]
		switch {
		case isOpen && !wasOpen:
			e.event(Event{Type: EventSystem, Severity: "info", Message: "maintenance started: " + mt.Title,
				Data: map[string]any{"msg": "maint_started", "title": mt.Title, "maint": mt.Name, "end": open[mt.Name], "monitors": len(mt.Monitors)}})
			if mt.Notify {
				e.systemNotice(now, e.text("maint_start", mt.Title, open[mt.Name], len(mt.Monitors)))
			}
		case !isOpen && wasOpen:
			e.event(Event{Type: EventSystem, Severity: "info", Message: "maintenance ended: " + mt.Title, Data: map[string]any{"msg": "maint_ended", "title": mt.Title, "maint": mt.Name}})
			if mt.Notify {
				e.systemNotice(now, e.text("maint_end", mt.Title))
			}
		}
	}
	prev := e.maintOf
	e.maintOf, e.maintOpen = active, open
	// Failures held during the window are notified when it closes.
	for name := range prev {
		if _, still := active[name]; still {
			continue
		}
		m, rt := e.monitors[name], e.states[name]
		if m == nil || rt == nil || rt.Dep != nil || rt.State.Avail != StatusCritical || !rt.State.Hard {
			continue
		}
		if inc := e.byMonitor[name]; inc == nil || !inc.State.Open() {
			e.childFailed(m, rt, e.policy(m.Policy), now)
		}
	}
}

// maintenanceIncident reports whether an incident belongs to a monitor in
// an open window (its notifications wait).
func (e *Engine) maintenanceIncident(inc *Incident) bool {
	return inc.Kind != IncidentSystem && e.inMaint(inc.Monitor)
}

// Maintenance lists the configured windows with their state.
func (e *Engine) Maintenance() []MaintView {
	var out []MaintView
	e.do(func() {
		now := e.now()
		loc := e.cfg.Location
		if loc == nil {
			loc = time.Local
		}
		for _, mt := range e.cfg.Maints {
			v := MaintView{Name: mt.Name, Title: mt.Title, Monitors: mt.Monitors}
			if start, end, ok := mt.Schedule.Active(now, loc); ok {
				v.Open, v.Start, v.End = true, start, end
			}
			if next, ok := mt.Schedule.Next(now, loc); ok {
				v.Next, v.NextEnd = next, next.Add(mt.Schedule.Duration)
			}
			out = append(out, v)
		}
	})
	return out
}
