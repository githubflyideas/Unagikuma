package server

import (
	"net/http"
	"sort"
	"time"
)

// savedAlerts lists the alerts not sent in the last seven days — held
// back during a local outage, cleared before confirmed, merged into a
// summary or stopped by the notification limit — with a count per day,
// so the overview can show what was spared and when. A team member sees
// only their team's monitors.
func (s *Server) savedAlerts(w http.ResponseWriter, r *http.Request) (any, error) {
	now := s.now()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -6)
	items, err := s.app.Store().SavedAlerts(since, 500)
	if err != nil {
		return nil, err
	}
	ts := teamOf(r)
	type day struct {
		Day    string         `json:"day"`
		Counts map[string]int `json:"counts"`
	}
	days := map[string]map[string]int{}
	for i := range 7 {
		days[since.AddDate(0, 0, i).Format("2006-01-02")] = map[string]int{}
	}
	shown := items[:0]
	for _, it := range items {
		if ts != nil && !ts.sees(it.Monitor) {
			continue
		}
		shown = append(shown, it)
		if d := days[it.At.In(now.Location()).Format("2006-01-02")]; d != nil {
			d[it.Name] += it.N
		}
	}
	// Counts of alerts kept before 2.6.11 listed them come from the daily
	// counters, for everyone but a team member.
	if ts == nil {
		if daily, err := s.app.Store().DailyCounters(since); err == nil {
			for d, names := range daily {
				if days[d] == nil {
					continue
				}
				for name, n := range names {
					if n > days[d][name] {
						days[d][name] = n
					}
				}
			}
		}
	}
	out := make([]day, 0, len(days))
	for d, c := range days {
		out = append(out, day{d, c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return map[string]any{"days": out, "items": shown}, nil
}
