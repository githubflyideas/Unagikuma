package server

import (
	"net/http"
	"sort"
	"sync"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Teams are isolated from each other: a member of one or more teams (who
// is not an administrator) sees only what those teams own, and changes it
// only as an operator. Monitors of other teams are not shown to them,
// except the state of a monitor their own monitors depend on.
//
// Isolation is deny by default: a team member may only call the routes in
// teamRoutes, each of which filters what it returns. A route added later
// stays closed to team members until it is reviewed and listed here.

type teamScope struct {
	roles      map[string]string // team -> role (operator, viewer)
	visible    map[string]bool   // monitors of the member's teams
	statusOnly map[string]bool   // monitors they depend on: state only
}

var errTeamOnly = Errorf(http.StatusForbidden, CodeForbidden, "not available to team members: ask an administrator")

// teamScopeFor builds the scope of a user with an account role from the
// configuration: nil for an administrator, and for everyone while there
// are no teams besides the built-in admin team (it owns every monitor).
// Otherwise the user works within their teams, with the role of their
// account; a user in no team is in the admin team.
func teamScopeFor(cfg *config.Config, user, role string) *teamScope {
	if role == store.RoleAdmin || !cfg.MultiTeam() {
		return nil
	}
	ts := &teamScope{roles: map[string]string{}, visible: map[string]bool{}, statusOnly: map[string]bool{}}
	teamRole := "viewer"
	if role == store.RoleOperator {
		teamRole = "operator"
	}
	for _, t := range cfg.TeamsOf(user) {
		ts.roles[t] = teamRole
	}
	for _, m := range cfg.Monitors {
		if ts.roles[m.TeamOf()] != "" {
			ts.visible[m.Name] = true
		}
	}
	// What their monitors depend on, all the way up: its state explains
	// why theirs is unreachable.
	var up func(m *config.Monitor)
	up = func(m *config.Monitor) {
		for _, p := range m.Parents {
			if ts.visible[p] || ts.statusOnly[p] {
				continue
			}
			if pm := cfg.FindMonitor(p); pm != nil {
				ts.statusOnly[p] = true
				up(pm)
			}
		}
	}
	for name := range ts.visible {
		up(cfg.FindMonitor(name))
	}
	return ts
}

// list is the monitors a member may see, for store queries; nil for a
// caller who sees everything.
func (ts *teamScope) list() *[]string {
	if ts == nil {
		return nil
	}
	out := make([]string, 0, len(ts.visible))
	for m := range ts.visible {
		out = append(out, m)
	}
	sort.Strings(out)
	return &out
}

// sees reports whether the member may see a monitor (not just its state).
func (ts *teamScope) sees(monitor string) bool { return ts.visible[monitor] }

// writes reports whether the member may change monitors of a team ("" is
// the built-in admin team).
func (ts *teamScope) writes(team string) bool {
	if team == "" {
		team = config.AdminTeam
	}
	return ts.roles[team] == "operator"
}

// writableTeams lists the teams whose monitors the member may change.
func (ts *teamScope) writableTeams() []string {
	var out []string
	for t := range ts.roles {
		if ts.writes(t) {
			out = append(out, t)
		}
	}
	return out
}

// scopes are the API scopes of a team member: reading, and acknowledging
// and changing for an operator. What they may change is then limited per
// monitor.
func (ts *teamScope) scopes() map[string]bool {
	if len(ts.writableTeams()) > 0 {
		return map[string]bool{"read": true, "ack": true, "write": true}
	}
	return map[string]bool{"read": true}
}

// seesMonitor reports whether the caller may see a monitor: always, unless
// they are a team member and it is not their teams'. Anything they may not
// see answers 404, as if it did not exist.
func seesMonitor(r *http.Request, name string) bool {
	ai := authOf(r)
	return ai == nil || ai.team == nil || ai.team.sees(name)
}

// seesIncident reports whether the caller may see an incident: one of a
// monitor they may see.
func (s *Server) seesIncident(r *http.Request, id int64) bool {
	if teamOf(r) == nil {
		return true
	}
	inc, err := s.app.Store().Incident(id)
	return err == nil && inc != nil && seesMonitor(r, inc.Monitor)
}

// incidentGate allows acting on an incident: 404 for one a team member may
// not see, 403 for one outside an account's sections or teams.
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

// teamOf is the caller's team scope, nil when they see everything.
func teamOf(r *http.Request) *teamScope {
	if ai := authOf(r); ai != nil {
		return ai.team
	}
	return nil
}

var errTeamChoice = Errorf(http.StatusForbidden, CodeForbidden, "choose one of your teams for this monitor: you may only add and change monitors of your own teams")

// teamOfNewRecord makes sure a team member writes monitors only into one
// of their teams: team= must name one, and is filled in when they have
// exactly one. Others are not limited; without team= a monitor goes to
// the built-in admin team. team=admin is written as no team= at all.
func teamOfNewRecord(ai *authInfo, spec *config.RecordSpec) error {
	idx := -1
	for i, o := range spec.Opts {
		if o.Key == "team" {
			idx = i
		}
	}
	defer func() {
		for i, o := range spec.Opts {
			if o.Key == "team" && (o.Value == config.AdminTeam || o.Value == "") {
				spec.Opts = append(spec.Opts[:i], spec.Opts[i+1:]...)
				break
			}
		}
	}()
	if ai == nil || ai.team == nil {
		return nil
	}
	if idx < 0 {
		w := ai.team.writableTeams()
		if len(w) != 1 {
			return errTeamChoice
		}
		spec.Opts = append(spec.Opts, config.Opt{Key: "team", Value: w[0]})
		return nil
	}
	if !ai.team.writes(spec.Opts[idx].Value) {
		return errTeamChoice
	}
	return nil
}

// teamRoutes are the routes open to team members; each filters by team.
var teamRoutes = map[string]bool{
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
	// Their overview, and the teams they are in.
	"GET /api/v1/overview": true,
	"GET /api/v1/teams":    true,
	"GET /api/v1/regions":  true,
}

// teamOverview is the overview a team member gets: their monitors and
// problems, and this host's own network state (which says whether a
// failure is on this side); nothing about channels, nodes, people or
// other teams.
func (s *Server) teamOverview(r *http.Request) map[string]any {
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
		"contact_issues": 0, "people": 0, "status_pages": 0, "team": true,
	}
}

// teamStream filters the live stream for a team member: events about
// monitors they may not see are left out, and events about no monitor
// (a configuration change, a banner) keep their type but lose their data,
// so the page knows to refresh without learning anything else. The scope
// follows configuration changes.
func (s *Server) teamStream(user, role string) sseFilter {
	var mu sync.Mutex
	serial := s.app.EffectiveConfig().Serial
	ts := teamScopeFor(s.app.EffectiveConfig(), user, role)
	return func(ev sseEvent) (sseEvent, bool) {
		mu.Lock()
		if cur := s.app.EffectiveConfig(); cur.Serial != serial {
			serial, ts = cur.Serial, teamScopeFor(cur, user, role)
		}
		scope := ts
		mu.Unlock()
		if scope == nil {
			return ev, true // no longer in any team: sees everything again
		}
		if ev.monitor != "" {
			return ev, scope.sees(ev.monitor)
		}
		ev.data = []byte("{}")
		return ev, true
	}
}
