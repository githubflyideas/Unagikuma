package server

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/store"
)

// The managers' summary (/summary): how available the services were over
// a period, compared with the period before, how many incidents there
// were and how fast they were fixed, which services need attention and
// what happened lately. Everything a reader can see, nothing to operate.

type summaryNow struct {
	Total    int            `json:"total"`
	Counts   map[string]int `json:"counts"` // by status, paused monitors as "paused"
	Problems []summaryItem  `json:"problems"`
}

type summaryItem struct {
	Name   string     `json:"name"`
	Title  string     `json:"title"`
	Status string     `json:"status,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
	Ratio  *float64   `json:"ratio,omitempty"`
	DownS  float64    `json:"down_s,omitempty"`
	Count  int        `json:"incidents,omitempty"`
}

type summaryDay struct {
	Day   time.Time `json:"day"`
	Ratio *float64  `json:"ratio"` // nil when nothing was watched that day
}

type summaryIncident struct {
	Title    string     `json:"title"`
	Monitor  string     `json:"monitor"`
	Opened   time.Time  `json:"opened"`
	Resolved *time.Time `json:"resolved,omitempty"`
}

type summaryJSON struct {
	Range        string            `json:"range"`
	From         time.Time         `json:"from"`
	To           time.Time         `json:"to"`
	Now          summaryNow        `json:"now"`
	Ratio        *float64          `json:"ratio"`
	PrevRatio    *float64          `json:"prev_ratio"`
	DownS        float64           `json:"down_s"`
	Incidents    int               `json:"incidents"`
	PrevIncident int               `json:"prev_incidents"`
	MTTR         float64           `json:"mttr_s"` // mean time to resolution, 0 when none resolved
	MTTA         float64           `json:"mtta_s"`
	Days         []summaryDay      `json:"days"`
	Attention    []summaryItem     `json:"attention"`
	Recent       []summaryIncident `json:"recent"`
}

// summaryCache keeps a result for a minute: a manager's page is opened
// often and the history queries scale with monitors × days.
var summaryCache struct {
	sync.Mutex
	m map[string]summaryEntry
}

type summaryEntry struct {
	at time.Time
	v  summaryJSON
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) (any, error) {
	key, d := "30d", ranges["30d"]
	switch q := r.URL.Query().Get("range"); q {
	case "":
	case "7d", "30d", "90d":
		key, d = q, ranges[q]
	default:
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "range is 7d, 30d or 90d")
	}
	var names []string
	titles := map[string]string{}
	for _, m := range s.app.EffectiveConfig().Monitors {
		if seesMonitor(r, m.Name) {
			names = append(names, m.Name)
			t := m.Title
			if t == "" {
				t = m.Name
			}
			titles[m.Name] = t
		}
	}
	now := s.now()
	ck := key + "|" + strings.Join(names, ",")
	summaryCache.Lock()
	if e, ok := summaryCache.m[ck]; ok && now.Sub(e.at) < time.Minute {
		summaryCache.Unlock()
		out := e.v
		out.Now = s.summaryNow(r, titles)
		return out, nil
	}
	summaryCache.Unlock()

	from := now.Add(-d)
	b := &summaryBuild{s: s, st: s.app.Store(), names: names, titles: titles, from: from, to: now, d: d}
	b.out = summaryJSON{Range: key, From: from, To: now, Now: s.summaryNow(r, titles), Days: []summaryDay{}, Attention: []summaryItem{}, Recent: []summaryIncident{}}
	if err := b.uptime(); err != nil {
		return nil, err
	}
	if err := b.days(); err != nil {
		return nil, err
	}
	b.responseTimes()
	b.recent(r)
	out := b.out

	summaryCache.Lock()
	if summaryCache.m == nil || len(summaryCache.m) > 64 {
		summaryCache.m = map[string]summaryEntry{}
	}
	summaryCache.m[ck] = summaryEntry{at: now, v: out}
	summaryCache.Unlock()
	return out, nil
}

// summaryBuild works out one summary for the monitors a reader sees.
type summaryBuild struct {
	s        *Server
	st       *store.Store
	names    []string
	titles   map[string]string
	from, to time.Time
	d        time.Duration
	out      summaryJSON
}

// availability is the share of up time, or nil without any.
func availability(up, down float64) *float64 {
	if up+down <= 0 {
		return nil
	}
	v := up / (up + down)
	return &v
}

// uptime adds up the period and the one before, and picks the five
// services that need attention most.
func (b *summaryBuild) uptime() error {
	st, from, now, d := b.st, b.from, b.to, b.d
	counts, _ := st.IncidentCounts(from, now)
	prevCounts, _ := st.IncidentCounts(from.Add(-d), from)
	var up, down, pUp, pDown float64
	var worst []summaryItem
	for _, n := range b.names {
		u, err := st.UptimeOf(n, from, now)
		if err != nil {
			return err
		}
		p, err := st.UptimeOf(n, from.Add(-d), from)
		if err != nil {
			return err
		}
		up, down, pUp, pDown = up+u.UpS, down+u.DownS, pUp+p.UpS, pDown+p.DownS
		b.out.Incidents += counts[n]
		b.out.PrevIncident += prevCounts[n]
		if r := b.s.shownRatio(u); r != nil && (*r < 1 || counts[n] > 0) {
			worst = append(worst, summaryItem{Name: n, Title: b.titles[n], Ratio: r, DownS: u.DownS, Count: counts[n]})
		}
	}
	b.out.Ratio, b.out.PrevRatio, b.out.DownS = availability(up, down), availability(pUp, pDown), down
	sort.SliceStable(worst, func(i, j int) bool {
		if *worst[i].Ratio != *worst[j].Ratio {
			return *worst[i].Ratio < *worst[j].Ratio
		}
		return worst[i].Count > worst[j].Count
	})
	if len(worst) > 5 {
		worst = worst[:5]
	}
	b.out.Attention = append(b.out.Attention, worst...)
	return nil
}

// days gives one point per day, in the server's time zone, oldest first.
func (b *summaryBuild) days() error {
	now := b.to
	days := int(b.d / (24 * time.Hour))
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for i := days - 1; i >= 0; i-- {
		a := today.AddDate(0, 0, -i)
		end := a.AddDate(0, 0, 1)
		if end.After(now) {
			end = now
		}
		var du, dd float64
		for _, n := range b.names {
			u, err := b.st.UptimeOf(n, a, end)
			if err != nil {
				return err
			}
			du, dd = du+u.UpS, dd+u.DownS
		}
		b.out.Days = append(b.out.Days, summaryDay{Day: a, Ratio: availability(du, dd)})
	}
	return nil
}

// responseTimes are the mean times to acknowledge and to resolve over the
// period, for the monitors this reader sees.
func (b *summaryBuild) responseTimes() {
	its, err := b.st.IncidentTimesBetween(b.from, b.to)
	if err != nil {
		return
	}
	var ackN, resN int
	var ackSum, resSum float64
	for _, it := range its {
		if _, ok := b.titles[it.Monitor]; !ok {
			continue
		}
		if !it.FirstAck.IsZero() {
			ackN++
			ackSum += it.FirstAck.Sub(it.Opened).Seconds()
		}
		if !it.Resolved.IsZero() {
			resN++
			resSum += it.Resolved.Sub(it.Opened).Seconds()
		}
	}
	if ackN > 0 {
		b.out.MTTA = ackSum / float64(ackN)
	}
	if resN > 0 {
		b.out.MTTR = resSum / float64(resN)
	}
}

// recent lists the last eight real incidents of the monitors this reader
// sees.
func (b *summaryBuild) recent(r *http.Request) {
	page, err := b.st.IncidentsOf("all", "", teamOf(r).list(), 0, 40, b.s.historySite(r))
	if err != nil {
		return
	}
	for _, row := range page.Items {
		if row.Kind != "normal" || row.State.Drill {
			continue
		}
		if _, ok := b.titles[row.Monitor]; !ok {
			continue
		}
		it := summaryIncident{Title: b.titles[row.Monitor], Monitor: row.Monitor, Opened: row.State.OpenedAt}
		if !row.State.ResolvedAt.IsZero() {
			t := row.State.ResolvedAt
			it.Resolved = &t
		}
		b.out.Recent = append(b.out.Recent, it)
		if len(b.out.Recent) == 8 {
			break
		}
	}
}

// summaryNow is the state right now (never cached).
func (s *Server) summaryNow(r *http.Request, titles map[string]string) summaryNow {
	n := summaryNow{Counts: map[string]int{}, Problems: []summaryItem{}}
	for _, row := range s.app.Engine().Rows() {
		if _, ok := titles[row.Name]; !ok {
			continue
		}
		n.Total++
		st := row.Status
		if row.Paused {
			st = "paused"
		}
		n.Counts[st]++
		if !row.Paused && (st == "critical" || st == "degraded") {
			it := summaryItem{Name: row.Name, Title: titles[row.Name], Status: st}
			if !row.LastChange.IsZero() {
				t := row.LastChange
				it.Since = &t
			}
			n.Problems = append(n.Problems, it)
		}
	}
	sort.SliceStable(n.Problems, func(i, j int) bool { return n.Problems[i].Status == "critical" && n.Problems[j].Status != "critical" })
	return n
}
