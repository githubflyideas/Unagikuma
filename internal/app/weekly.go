package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// The weekly report looks back over seven days, for the people who do not
// read every alert: how available the monitors were, how many problems
// there were and for how long, which monitors did worst, and how many
// alerts were held back. It goes to the same teams and channels as the
// daily report.

const weeklyLastKey = "weekly_report_last"

// weeklyWorst is how many of the least available monitors are listed.
const weeklyWorst = 10

func (a *App) runWeeklyReport(ctx context.Context) {
	a.every(ctx, time.Minute, func() { a.weeklyReportDue(a.now()) })
}

// weeklyReportDue sends the report once its day and time have come, once
// a week (a restart in between does not send it twice).
func (a *App) weeklyReportDue(now time.Time) {
	if a.editable() != nil || a.engine == nil {
		return // a node of a cluster: its master sends the reports
	}
	ts := a.Config().Config.Tunables
	if !ts.Bool("notify.weekly-report") {
		return
	}
	loc := a.location()
	day, at := ts.WeekTime("notify.weekly-report-at")
	local := now.In(loc)
	if local.Weekday() != day || local.Hour()*60+local.Minute() < at {
		return
	}
	today := local.Format("2006-01-02")
	if last, _ := a.store.Setting(weeklyLastKey); last == today {
		return
	}
	_ = a.store.SetSetting(weeklyLastKey, today)
	for _, r := range a.WeeklyReports(now) {
		a.engine.SendReport(r)
	}
}

func (a *App) location() *time.Location {
	if a.opts.Location != nil {
		return a.opts.Location
	}
	return time.Local
}

// WeeklyReports renders the weekly report of each team receiving reports.
func (a *App) WeeklyReports(now time.Time) []engine.Report {
	var out []engine.Report
	for _, tg := range a.engine.ReportTargets() {
		r := a.weeklyReport(now, tg.Team)
		r.Policy = tg.Policy
		out = append(out, r)
	}
	return out
}

func (a *App) weeklyReport(now time.Time, team string) engine.Report {
	lang := a.messageLang()
	loc := a.location()
	from := now.Add(-7 * 24 * time.Hour)
	type avail struct {
		name  string
		ratio float64
	}
	var list []avail
	var sum, down float64
	mine := map[string]bool{}
	for _, m := range a.EffectiveConfig().Monitors {
		if m.Disabled || (team != "" && m.Team != team) {
			continue
		}
		mine[m.Name] = true
		u, err := a.store.UptimeOf(m.Name, from, now)
		if err != nil || u.Ratio == nil {
			continue
		}
		title := m.Title
		if title == "" {
			title = m.Name
		}
		list = append(list, avail{title, *u.Ratio})
		sum += *u.Ratio
		down += u.DownS
	}
	problems := 0
	if page, err := a.store.Incidents("all", "", 0, 5000); err == nil {
		for _, inc := range page.Items {
			if inc.Monitor != "" && mine[inc.Monitor] && !inc.State.OpenedAt.Before(from) && inc.State.OpenedAt.Before(now) {
				problems++
			}
		}
	}
	saved := 0
	if team == "" { // held back across the installation, not per team
		if days, err := a.store.DailyCounters(from); err == nil {
			for _, d := range days {
				for _, n := range d {
					saved += n
				}
			}
		}
	}
	pct := func(r float64) string {
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", r*100), "0"), ".0") + "%"
	}
	mean := 1.0
	if len(list) > 0 {
		mean = sum / float64(len(list))
	}
	span := from.In(loc).Format("01-02") + " – " + now.In(loc).Format("01-02")
	r := engine.Report{Team: team, OK: problems == 0}
	r.Title = i18n.T(lang, "sys.weekly_title", "span", span, "avail", pct(mean), "n", problems)
	lines := []string{
		i18n.T(lang, "sys.weekly_avail", "avail", pct(mean), "monitors", len(list)),
		i18n.T(lang, "sys.weekly_problems", "n", problems, "down", i18n.Duration(time.Duration(down)*time.Second, lang)),
	}
	if saved > 0 {
		lines = append(lines, i18n.T(lang, "sys.weekly_saved", "n", saved))
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].ratio < list[j].ratio })
	var worst []string
	for _, x := range list {
		if x.ratio >= 1 || len(worst) == weeklyWorst {
			break
		}
		worst = append(worst, "• "+x.name+": "+pct(x.ratio))
	}
	if len(worst) > 0 {
		lines = append(lines, "", i18n.T(lang, "sys.weekly_worst"))
		lines = append(lines, worst...)
	} else {
		lines = append(lines, "", i18n.T(lang, "sys.weekly_all_up"))
	}
	r.Body = strings.Join(lines, "\n")
	return r
}
