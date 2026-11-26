package engine

import (
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostview"
	"github.com/githubflyideas/unagikuma/internal/notify"
)

// Report is the daily status report: what is down right now, what is
// fine, rendered in the configured language.
type Report struct {
	Title  string
	Body   string
	Policy string
	OK     bool // every host up and every service OK
}

// scheduleReport computes the next daily status report: the soonest of
// the day's report times.
func (e *Engine) scheduleReport(now time.Time) {
	e.nextReport = time.Time{}
	for _, t := range e.cfg.Tunables.TimesOfDay("notify.daily-report-at") {
		if next := NextDaily(now, t/60, t%60, e.cfg.Location); e.nextReport.IsZero() || next.Before(e.nextReport) {
			e.nextReport = next
		}
	}
}

// dailyReport sends the status report at each of the configured times,
// also when everything is fine: silence is not proof the system works.
func (e *Engine) dailyReport(now time.Time) {
	if e.nextReport.IsZero() || now.Before(e.nextReport) {
		return
	}
	e.scheduleReport(now)
	if !e.cfg.Tunables.Bool("notify.daily-report") {
		return
	}
	for _, r := range e.reports(now) {
		e.sendReport(r, now)
	}
}

// reports renders the report: every monitor, through the report policy.
func (e *Engine) reports(now time.Time) []Report {
	return []Report{e.report(now, e.reportPolicy())}
}

// ReportPolicy is the policy the reports go through.
func (e *Engine) ReportPolicy() string {
	var out string
	e.do(func() { out = e.reportPolicy() })
	return out
}

// SendReport sends a report made elsewhere (the weekly report) the way the
// daily one goes.
func (e *Engine) SendReport(r Report) {
	e.do(func() { e.sendReport(r, e.now()) })
}

func (e *Engine) sendReport(r Report, now time.Time) {
	e.enqueue(&Intent{Kind: "REPORT", Policy: r.Policy, CreatedAt: now, DueAt: now, System: true,
		Message: notify.Message{Kind: "REPORT", Priority: "high", Title: r.Title, Body: r.Body, Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang}})
}

// DailyReport renders the report as it would be sent now.
func (e *Engine) DailyReport() Report {
	var r Report
	e.do(func() { r = e.reports(e.now())[0] })
	return r
}

// SendDailyReport sends the report now, outside its schedule.
func (e *Engine) SendDailyReport() Report {
	var r Report
	e.do(func() {
		now := e.now()
		r = e.reports(now)[0]
		e.sendReport(r, now)
	})
	return r
}

// SystemMonitors are the monitors the installation adds for its own
// connectivity (the gateways): reports leave them out of the hosts and
// services they count, the local-outage logic watches them.
var SystemMonitors = map[string]bool{"lan-gateway": true, "internet-gateway": true}

func (e *Engine) report(now time.Time, policy string) Report {
	max := e.cfg.Tunables.Int("notify.daily-report-max-items")
	rows := map[string]MonitorRow{}
	var svcs []hostview.Service
	for _, n := range e.sortedMonitorNames() {
		r, ok := e.row(n, now)
		if !ok {
			continue
		}
		rows[n] = r
		m := e.monitors[n]
		if SystemMonitors[n] {
			continue
		}
		host := m.Host
		if host == "" {
			host = n
		}
		svcs = append(svcs, hostview.Service{Name: n, Host: host, Address: m.Address, Area: m.Area, Group: m.Group, Ping: m.Ping, Status: r.Status})
	}
	hosts := hostview.Build(svcs)
	byState := map[string][]string{}
	for _, h := range hosts {
		byState[h.Status] = append(byState[h.Status], h.Name)
	}
	counts := map[string]int{}
	var bad []MonitorRow
	for _, s := range svcs {
		counts[s.Status]++
		switch s.Status {
		case StatusCritical, StatusDegraded, StatusUnknown, StatusUnreachable:
			bad = append(bad, rows[s.Name])
		}
	}
	// Worst first, then the longest standing.
	sort.SliceStable(bad, func(i, j int) bool {
		a, b := hostview.Severity[bad[i].Status], hostview.Severity[bad[j].Status]
		if a != b {
			return a > b
		}
		if !bad[i].LastChange.Equal(bad[j].LastChange) {
			return bad[i].LastChange.Before(bad[j].LastChange)
		}
		return bad[i].Name < bad[j].Name // failed together: by name, not by chance
	})

	list := func(names []string) string {
		if len(names) > max {
			return strings.Join(names[:max], ", ") + " " + e.text("report_more", len(names)-max)
		}
		return strings.Join(names, ", ")
	}
	var b []string
	section := func(key string, names []string) {
		if len(names) > 0 {
			b = append(b, e.text(key, len(names), list(names)))
		}
	}
	section("report_hosts_down", byState["down"])
	section("report_hosts_unreachable", byState["unreachable"])
	if len(bad) > 0 {
		b = append(b, "", e.text("report_services", len(bad)))
		for i, r := range bad {
			if i == max {
				b = append(b, e.text("report_more", len(bad)-max))
				break
			}
			title := r.Title
			if title == "" {
				title = r.Name
			}
			who := ""
			if inc, ok := e.incidents[r.IncidentID]; ok && inc.State.Acked {
				who = inc.State.AckedBy
			}
			b = append(b, e.text("report_service_line", title, r.Status, r.LastChange, now.Sub(r.LastChange), who, r.Detail))
		}
		b = append(b, "")
	}
	section("report_hosts_maintenance", byState["maintenance"])
	section("report_hosts_paused", byState["paused"])
	section("report_hosts_pending", byState["pending"])
	section("report_hosts_up", byState["up"])
	b = append(b, "", e.text("report_summary", len(hosts), len(svcs), counts))

	r := Report{Policy: policy}
	down := len(byState["down"]) + len(byState["unreachable"])
	r.OK = down == 0 && len(bad) == 0
	if r.OK {
		r.Title = e.text("report_title_ok", len(hosts), len(svcs))
	} else {
		r.Title = e.text("report_title", down, len(hosts), len(bad), len(svcs))
	}
	// One blank line between blocks, however many sections were empty.
	var lines []string
	for _, l := range b {
		if l == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue
		}
		lines = append(lines, l)
	}
	r.Body = strings.TrimSpace(strings.Join(lines, "\n"))
	return r
}

func (e *Engine) reportPolicy() string {
	if p := strings.TrimSpace(e.cfg.Tunables.Raw("notify.daily-report-policy")); p != "" {
		if _, ok := e.cfg.Policies[p]; ok {
			return p
		}
	}
	// The alert channels, as the admin team's channels were before 3.0;
	// without any, the system channels.
	if p, ok := e.cfg.Policies["default"]; ok && len(p.Chain) > 0 {
		return "default"
	}
	return e.policy("system").Name
}

// Announce sends a free-form notice (a planned change, its reminders and
// its outcome) through a policy, or to the given channels at once. It is
// delivered and recorded like any other notification.
func (e *Engine) Announce(kind, policy string, channels []string, title, body string) {
	e.do(func() {
		now := e.now()
		pol := policy
		if pol == "" {
			pol = "system"
		}
		e.enqueue(&Intent{Kind: kind, Policy: pol, Chain: append([]string(nil), channels...), CreatedAt: now, DueAt: now, System: true,
			Message: notify.Message{Kind: kind, Priority: "high", Title: title, Body: body, Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang}})
	})
}
