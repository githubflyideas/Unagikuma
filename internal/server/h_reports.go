package server

import (
	"github.com/githubflyideas/unagikuma/internal/config"
	"net/http"
	"sort"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
)

// tactical summarises hosts and services by state, split into unhandled
// and handled (acknowledged, silenced, in maintenance) like an operations
// console needs it.
func (s *Server) tactical(req *http.Request) map[string]any {
	cfg := s.app.EffectiveConfig()
	incs := s.incidentIndex()
	type split struct {
		Total     int `json:"total"`
		Unhandled int `json:"unhandled"`
		Handled   int `json:"handled"`
	}
	services := map[string]*split{}
	for _, r := range s.app.Engine().Rows() {
		if !seesMonitor(req, r.Name) {
			continue
		}
		row := s.rowBase(r, cfg, incs)
		sp := services[row.Status]
		if sp == nil {
			sp = &split{}
			services[row.Status] = sp
		}
		sp.Total++
		handled := row.Incident != nil && (row.Incident.Acked || row.Incident.SilencedUntil != nil)
		if handled {
			sp.Handled++
		} else {
			sp.Unhandled++
		}
	}
	hosts := map[string]int{}
	for _, h := range s.hostList(req) {
		hosts[h.Status]++
	}
	return map[string]any{"services": services, "hosts": hosts}
}

// availability reports uptime and downtime per monitor over a range.
func (s *Server) availability(w http.ResponseWriter, r *http.Request) (any, error) {
	key, d := rangeOf(r)
	if key == "24h" && r.URL.Query().Get("range") == "" {
		key, d = "30d", ranges["30d"]
	}
	now := s.now()
	from := now.Add(-d)
	type row struct {
		Name      string   `json:"name"`
		Title     string   `json:"title"`
		Host      string   `json:"host"`
		Group     string   `json:"group,omitempty"`
		Area      string   `json:"area,omitempty"`
		Status    string   `json:"status"`
		Ratio     *float64 `json:"ratio"`
		Coverage  float64  `json:"coverage"`
		DegradedS float64  `json:"degraded_s"`
		DownS     float64  `json:"down_s"`
		ExcludedS float64  `json:"excluded_s"`
		Incidents int      `json:"incidents"`
	}
	cfg := s.app.EffectiveConfig()
	counts, _ := s.app.Store().IncidentCounts(from, now)
	status := map[string]string{}
	for _, r := range s.app.Engine().Rows() {
		status[r.Name] = r.Status
	}
	out := []row{}
	for _, m := range cfg.Monitors {
		if !seesMonitor(r, m.Name) {
			continue
		}
		u, err := s.app.Store().UptimeOf(m.Name, from, now)
		if err != nil {
			return nil, err
		}
		out = append(out, row{Name: m.Name, Title: m.Title, Host: m.HostKey(), Group: m.Group, Area: m.Area, Status: status[m.Name],
			Ratio: s.shownRatio(u), Coverage: u.Coverage, DegradedS: u.DegradedS, DownS: u.DownS, ExcludedS: u.ExcludedS, Incidents: counts[m.Name]})
	}
	// Worst first: that is what a report is read for.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := pf(out[i].Ratio), pf(out[j].Ratio)
		if a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return map[string]any{"range": key, "from": from, "to": now, "items": out, "generated": now.Format(time.RFC3339)}, nil
}

type dailyJSON struct {
	Team    string `json:"team"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Policy  string `json:"policy"`
	OK      bool   `json:"ok"`
	Enabled bool   `json:"enabled"`
	At      string `json:"at"`
	// Teams lists every team and whether it receives the report.
	Teams []reportTeamJSON `json:"teams"`
	Sent  int              `json:"sent,omitempty"`
}

type reportTeamJSON struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Receives bool   `json:"receives"`
}

func (s *Server) daily(team string, r engine.Report) dailyJSON {
	cfg := s.app.EffectiveConfig()
	ts := cfg.Tunables
	if team == "" {
		team = config.AdminTeam
	}
	out := dailyJSON{Team: team, Title: r.Title, Body: r.Body, Policy: r.Policy, OK: r.OK, Enabled: ts.Bool("notify.daily-report"), At: ts.Raw("notify.daily-report-at"), Teams: []reportTeamJSON{}}
	receives := map[string]bool{}
	for _, n := range s.app.Engine().ReportTeams() {
		receives[n] = true
	}
	for _, t := range cfg.AllTeams() {
		title := t.Title
		if title == "" {
			title = t.Name
		}
		out.Teams = append(out.Teams, reportTeamJSON{Name: t.Name, Title: title, Receives: receives[t.Name]})
	}
	return out
}

// reportTeam reads the team a report is asked for ("" = the admin team).
func (s *Server) reportTeam(team string) (string, error) {
	if team == "" {
		return config.AdminTeam, nil
	}
	if s.app.EffectiveConfig().FindTeam(team) == nil {
		return "", notFound("team")
	}
	return team, nil
}

// dailyReport previews a team's daily status report as it would be sent now.
func (s *Server) dailyReport(w http.ResponseWriter, r *http.Request) (any, error) {
	team, err := s.reportTeam(r.URL.Query().Get("team"))
	if err != nil {
		return nil, err
	}
	return s.daily(team, s.app.Engine().DailyReport(team)), nil
}

// sendDailyReport sends a team's daily status report now, or every
// team's that receives one when none is named.
func (s *Server) sendDailyReport(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Team string `json:"team"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, s.maxBody(), &req); err != nil {
			return nil, err
		}
	}
	if req.Team != "" {
		if _, err := s.reportTeam(req.Team); err != nil {
			return nil, err
		}
	}
	sent := s.app.Engine().SendDailyReport(req.Team)
	var first engine.Report
	if len(sent) > 0 {
		first = sent[0]
	}
	out := s.daily(req.Team, first)
	out.Sent = len(sent)
	return out, nil
}

// expiry lists every certificate and domain registration the checks
// watch, soonest first.
func (s *Server) expiry(w http.ResponseWriter, r *http.Request) (any, error) {
	type row struct {
		Monitor string  `json:"monitor"`
		Title   string  `json:"title"`
		What    string  `json:"what"` // cert | domain
		Target  string  `json:"target"`
		Days    float64 `json:"days"`
		Status  string  `json:"status"`
	}
	out := []row{}
	for _, m := range s.app.Engine().Rows() {
		if !seesMonitor(r, m.Name) {
			continue
		}
		for what, key := range map[string]string{"cert": "cert.days", "domain": "domain.days"} {
			if d, ok := m.Metrics[key]; ok {
				out = append(out, row{m.Name, m.Title, what, m.Target, d, m.Status})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Days < out[j].Days })
	return map[string]any{"items": out}, nil
}
