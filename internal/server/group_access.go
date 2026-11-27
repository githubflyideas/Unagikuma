package server

import (
	"net/http"
	"sort"
	"sync"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Groups: an account that is not an administrator sees only the monitors
// in the groups it was given (group= on a monitor), and changes them only
// as an operator. Monitors with no group are visible only to
// administrators. Other monitors are not shown, except the state of a
// monitor its own ones depend on. An account given no groups sees no
// monitors.
//
// Limiting is deny by default: such an account may only call the routes in
// groupRoutes, each of which filters what it returns. A route added later
// stays closed to it until it is reviewed and listed here.

type groupScope struct {
	groups     map[string]bool // the account's groups
	operator   bool            // may change monitors in them
	visible    map[string]bool // monitors in the groups
	statusOnly map[string]bool // monitors they depend on: state only
}

var errGroupOnly = Errorf(http.StatusForbidden, CodeForbidden, "not available to an account limited to some groups: ask an administrator")

// groupScopeFor builds the scope of an account: nil for an administrator
// (it sees everything, including monitors with no group). Any other
// account sees only the monitors in the groups it was given.
func groupScopeFor(cfg *config.Config, role string, groups []string) *groupScope {
	if role == store.RoleAdmin {
		return nil
	}
	gs := &groupScope{groups: map[string]bool{}, operator: role == store.RoleOperator, visible: map[string]bool{}, statusOnly: map[string]bool{}}
	for _, g := range groups {
		if g != "" {
			gs.groups[g] = true
		}
	}
	for _, m := range cfg.Monitors {
		if m.Group != "" && gs.groups[m.Group] {
			gs.visible[m.Name] = true
		}
	}
	// What their monitors depend on, all the way up: its state explains
	// why theirs is unreachable.
	var up func(m *config.Monitor)
	up = func(m *config.Monitor) {
		for _, p := range m.Parents {
			if gs.visible[p] || gs.statusOnly[p] {
				continue
			}
			if pm := cfg.FindMonitor(p); pm != nil {
				gs.statusOnly[p] = true
				up(pm)
			}
		}
	}
	for name := range gs.visible {
		up(cfg.FindMonitor(name))
	}
	return gs
}

// list is the monitors a member may see, for store queries; nil for a
// caller who sees everything.
func (gs *groupScope) list() *[]string {
	if gs == nil {
		return nil
	}
	out := make([]string, 0, len(gs.visible))
	for m := range gs.visible {
		out = append(out, m)
	}
	sort.Strings(out)
	return &out
}

// sees reports whether the member may see a monitor (not just its state).
func (gs *groupScope) sees(monitor string) bool { return gs.visible[monitor] }

// writes reports whether the account may change monitors of a group.
func (gs *groupScope) writes(group string) bool { return gs.operator && gs.groups[group] }

// writableGroups lists the groups whose monitors the account may change.
func (gs *groupScope) writableGroups() []string {
	var out []string
	for g := range gs.groups {
		if gs.writes(g) {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

// scopes are the API scopes of such an account: reading, and acknowledging
// and changing for an operator. What it may change is then limited per
// monitor.
func (gs *groupScope) scopes() map[string]bool {
	if gs.operator {
		return map[string]bool{"read": true, "ack": true, "write": true}
	}
	return map[string]bool{"read": true}
}

// seesMonitor reports whether the caller may see a monitor: always, unless
// they are limited to groups and it is in none of theirs. Anything they may not
// see answers 404, as if it did not exist.
func seesMonitor(r *http.Request, name string) bool {
	ai := authOf(r)
	return ai == nil || ai.group == nil || ai.group.sees(name)
}

// seesIncident reports whether the caller may see an incident: one of a
// monitor they may see.
func (s *Server) seesIncident(r *http.Request, id int64) bool {
	if groupOf(r) == nil {
		return true
	}
	inc, err := s.app.Store().Incident(id)
	return err == nil && inc != nil && seesMonitor(r, inc.Monitor)
}

// incidentGate allows acting on an incident: 404 for one the caller may
// not see, 403 for one outside an account's sections or groups.
func (s *Server) incidentGate(r *http.Request, id int64) error {
	if !s.seesIncident(r, id) {
		return notFound("incident")
	}
	if !s.allowsIncident(authOf(r), id) {
		return errOutsideAreas
	}
	return nil
}

// seesAll reports whether the caller may see every one of the monitors,
// and there is at least one.
func seesAll(r *http.Request, names []string) bool {
	for _, n := range names {
		if !seesMonitor(r, n) {
			return false
		}
	}
	return len(names) > 0
}

// groupOf is the caller's group scope, nil when they see everything.
func groupOf(r *http.Request) *groupScope {
	if ai := authOf(r); ai != nil {
		return ai.group
	}
	return nil
}

var errGroupChoice = Errorf(http.StatusForbidden, CodeForbidden, "choose one of your groups for this monitor: you may only add and change monitors of your own groups")

// groupOfNewRecord makes sure an account limited to groups writes monitors
// only into one of them: group= must name one, and is filled in when it
// has exactly one. Others are not limited.
func groupOfNewRecord(ai *authInfo, spec *config.RecordSpec) error {
	if ai == nil || ai.group == nil {
		return nil
	}
	for _, o := range spec.Opts {
		if o.Key == "group" {
			if !ai.group.writes(o.Value) {
				return errGroupChoice
			}
			return nil
		}
	}
	w := ai.group.writableGroups()
	if len(w) != 1 {
		return errGroupChoice
	}
	spec.Opts = append(spec.Opts, config.Opt{Key: "group", Value: w[0]})
	return nil
}

// groupRoutes are the routes open to accounts limited to groups; each filters.
var groupRoutes = map[string]bool{
	// Their own session and preferences.
	"GET /api/v1/session":          true,
	"GET /api/v1/saved-alerts":     true,
	"DELETE /api/v1/session":       true,
	"PUT /api/v1/session/password": true,
	"GET /api/v1/preferences":      true,
	"PUT /api/v1/preferences":      true,
	"GET /api/v1/languages":        true,
	"GET /api/v1/keywords":         true, // the UDP keyword catalogue: no one's data
	// Monitors: lists and details are filtered, changes go through
	// allowsMonitor.
	"GET /api/v1/monitors":                     true,
	"GET /api/v1/monitors/heartbeats":          true,
	"GET /api/v1/monitors/{name}":              true,
	"GET /api/v1/monitors/{name}/latency":      true,
	"GET /api/v1/monitors/{name}/timeline":     true,
	"GET /api/v1/monitors/{name}/push":         true,
	"POST /api/v1/monitors/{name}/push/rotate": true,
	"POST /api/v1/monitors":                    true,
	"PUT /api/v1/monitors/{name}":              true,
	"DELETE /api/v1/monitors/{name}":           true,
	"POST /api/v1/monitors/bulk-delete":        true,
	"POST /api/v1/monitors/bulk-pause":         true,
	"POST /api/v1/monitors/{name}/check":       true,
	"POST /api/v1/monitors/{name}/pause":       true,
	"POST /api/v1/monitors/same-check":         true,
	"POST /api/v1/probe":                       true,
	"POST /api/v1/quickadd/parse":              true,
	// Incidents and events of their monitors.
	"GET /api/v1/incidents":               true,
	"GET /api/v1/incidents/{id}/report":   true,
	"POST /api/v1/incidents/{id}/ack":     true,
	"DELETE /api/v1/incidents/{id}/ack":   true,
	"POST /api/v1/incidents/{id}/silence": true,
	"POST /api/v1/incidents/bulk":         true,
	"GET /api/v1/events":                  true,
	// Reports of their monitors, and their maintenance windows.
	"GET /api/v1/hosts":                 true,
	"GET /api/v1/reports/availability":  true,
	"GET /api/v1/expiry":                true,
	"GET /api/v1/maintenance":           true,
	"POST /api/v1/maintenance":          true,
	"DELETE /api/v1/maintenance/{name}": true,
	// Their overview.
	"GET /api/v1/overview": true,
	"GET /api/v1/regions":  true,
}

// groupOverview is the overview an account limited to groups gets: their monitors and
// problems, and this host's own network state (which says whether a
// failure is on this side); nothing about channels, nodes, people or
// other groups.
func (s *Server) groupOverview(r *http.Request) map[string]any {
	ov := s.app.Engine().Overview()
	now := s.now()
	counts := map[string]int{"total": 0}
	degraded := 0
	for _, row := range s.app.Engine().Rows() {
		if !seesMonitor(r, row.Name) {
			continue
		}
		counts[string(row.Status)]++
		counts["total"]++
		if row.Status == engine.StatusDegraded {
			degraded++
		}
	}
	unhandled := 0
	for _, inc := range s.app.Engine().OpenIncidents() {
		if inc.State.Open() && !inc.State.Drill && seesMonitor(r, inc.Monitor) && engine.Unhandled(&inc, now) {
			unhandled++
		}
	}
	// This host's own health is the same for everyone: it tells whether a
	// failure is on this side. The external heartbeat's address is left
	// out (it can carry a token).
	h := ov.Health
	hb := s.app.HeartbeatStatus()
	health := map[string]any{
		"uplink":      map[string]any{"state": h.Uplink, "since": tp(h.UplinkSince), "suspected": h.Suspected},
		"gateway":     map[string]any{"addr": h.Gateway, "ok": h.GatewayOK, "state": h.GatewayState, "rtt_ms": float64(h.GatewayRTT) / 1e6, "note": h.GatewayNote},
		"exit":        exitJSON(h),
		"dns_down":    h.DNSDown,
		"heartbeat":   map[string]any{"configured": hb.Configured, "last_ok": tp(hb.LastOK)},
		"anomalies":   ov.Anomalies,
		"interrupted": nil,
	}
	if in := ov.LastInterruption; in != nil {
		health["interrupted"] = map[string]any{"at": in.Start, "kind": in.Kind, "duration_s": int(in.Duration.Seconds())}
	}
	type exp struct {
		Monitor string  `json:"monitor"`
		Title   string  `json:"title"`
		Days    float64 `json:"days"`
		What    string  `json:"what"`
	}
	expiring := []exp{}
	certWin := float64(s.app.EffectiveConfig().Tunables.Int("probe.cert-warn-days"))
	if certWin <= 0 {
		certWin = 62
	}
	for _, row := range s.app.Engine().Rows() {
		if !seesMonitor(r, row.Name) {
			continue
		}
		for what, key := range map[string]string{"cert": "cert.days", "domain": "domain.days"} {
			win := 30.0
			if what == "cert" {
				win = certWin
			}
			if d, ok := row.Metrics[key]; ok && d < win {
				expiring = append(expiring, exp{row.Name, row.Title, d, what})
			}
		}
	}
	sort.Slice(expiring, func(i, j int) bool { return expiring[i].Days < expiring[j].Days })
	return map[string]any{
		"instance": s.app.Instance(), "serial": ov.Serial, "layout": "single", "counts": counts, "unhandled": unhandled,
		"degraded": degraded, "health": health, "channels": []any{}, "expiring": expiring, "open_system": 0,
		"alerting_configured": true, "nodes": []any{}, "paths": []any{}, "tactical": s.tactical(r), "script_problems": 0,
		"contact_issues": 0, "people": 0, "status_pages": 0, "limited": true,
	}
}

// groupStream filters the live stream for an account limited to groups: events about
// monitors they may not see are left out, and events about no monitor
// (a configuration change, a banner) keep their type but lose their data,
// so the page knows to refresh without learning anything else. The scope
// follows configuration changes.
func (s *Server) groupStream(role string, groups []string) sseFilter {
	var mu sync.Mutex
	serial := s.app.EffectiveConfig().Serial
	ts := groupScopeFor(s.app.EffectiveConfig(), role, groups)
	return func(ev sseEvent) (sseEvent, bool) {
		mu.Lock()
		if cur := s.app.EffectiveConfig(); cur.Serial != serial {
			serial, ts = cur.Serial, groupScopeFor(cur, role, groups)
		}
		scope := ts
		mu.Unlock()
		if scope == nil {
			return ev, true // sees everything
		}
		if ev.monitor != "" {
			return ev, scope.sees(ev.monitor)
		}
		ev.data = []byte("{}")
		return ev, true
	}
}
