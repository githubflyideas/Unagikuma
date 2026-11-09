package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// published returns the monitors a status page shows. Its select= is a
// configuration selector (static fields, checked when the file is
// loaded), matched against the monitors as configured — not the UI's
// search syntax, which reads live fields.
func (s *Server) published(sp *config.StatusPage) []rowJSON {
	cfg := s.app.EffectiveConfig()
	sel, err := config.ParseSelector(sp.Select)
	if err != nil {
		return nil
	}
	var out []rowJSON
	for _, m := range cfg.Monitors {
		if !sel.Match(m) {
			continue
		}
		r, ok := s.app.Engine().Row(m.Name)
		if !ok { // Configured elsewhere: preserve selector coverage without inventing a local verdict.
			r = engine.MonitorRow{Name: m.Name, Title: m.Title, Type: m.Type, Status: engine.StatusUnknown}
		}
		out = append(out, s.rowBase(r, cfg, nil))
	}
	return out
}

func (s *Server) statusPage(name string) *config.StatusPage {
	for _, sp := range s.app.EffectiveConfig().StatusPages {
		if sp.Name == name || strings.TrimPrefix(sp.Path, "/status/") == name {
			return sp
		}
	}
	return nil
}

// statusPageJSON is public: only titles, statuses and uptime of the
// selected monitors, never targets or reasons.
func (s *Server) statusPageJSON(w http.ResponseWriter, r *http.Request) {
	sp := s.pageFor(r)
	if sp == nil {
		writeError(w, notFound("status page"))
		return
	}
	// Public and unauthenticated: compute each page at most every
	// statusPageTTL however often it is requested.
	body := s.pageCache.get(sp.Name, s.now(), func() []byte { return s.statusPageBody(sp) })
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30")
	_, _ = w.Write(body)
}

const statusPageTTL = 30 * time.Second

// pageCache keeps rendered public pages for statusPageTTL.
type pageCache struct {
	mu sync.Mutex
	m  map[string]cachedPage
}

type cachedPage struct {
	at   time.Time
	body []byte
}

func (c *pageCache) get(key string, now time.Time, build func() []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.m[key]; ok && now.Sub(p.at) < statusPageTTL && !now.Before(p.at) {
		return p.body
	}
	if c.m == nil {
		c.m = map[string]cachedPage{}
	}
	b := build()
	c.m[key] = cachedPage{now, b}
	return b
}

func (s *Server) statusPageBody(sp *config.StatusPage) []byte {
	now := s.now()
	type mon struct {
		Title  string              `json:"title"`
		Group  string              `json:"group,omitempty"`
		Status string              `json:"status"`
		Uptime map[string]*float64 `json:"uptime"`
		Cells  []store.Cell        `json:"cells"`
	}
	var mons []mon
	worst := "ok"
	for _, rj := range s.published(sp) {
		m := mon{Title: rj.Title, Group: rj.Group, Status: rj.Status, Uptime: map[string]*float64{}}
		if m.Status == "critical" || (m.Status == "degraded" && worst == "ok") {
			worst = m.Status
		}
		for _, wnd := range sp.Windows {
			if d, ok := ranges[wnd]; ok {
				u, _ := s.app.Store().UptimeOf(rj.Name, now.Add(-d), now)
				m.Uptime[wnd] = s.shownRatio(u)
			}
		}
		m.Cells, _ = s.app.Store().Cells(rj.Name, now.Add(-90*24*time.Hour), now, 90)
		mons = append(mons, m)
	}
	if mons == nil {
		mons = []mon{}
	}
	anns, _ := s.app.Store().Announcements(sp.Name, now.Add(-s.app.EffectiveConfig().Tunables.Duration("statuspage.resolved-shown")), 20)
	for i := range anns {
		anns[i].Author, anns[i].Page = "", "" // public
	}
	if anns == nil {
		anns = []store.Announcement{}
	}
	// Maintenance of the page's monitors, now and within a week.
	type maint struct {
		Title    string    `json:"title"`
		Start    time.Time `json:"start"`
		End      time.Time `json:"end,omitzero"`
		Open     bool      `json:"open"`
		Monitors []string  `json:"monitors"`
	}
	titles := map[string]string{}
	for _, rj := range s.published(sp) {
		titles[rj.Name] = rj.Title
	}
	maints := []maint{}
	for _, mv := range s.app.Engine().Maintenance() {
		var on []string
		for _, n := range mv.Monitors {
			if t, ok := titles[n]; ok {
				on = append(on, t)
			}
		}
		start, end := mv.Start, mv.End
		if !mv.Open {
			start, end = mv.Next, mv.NextEnd
		}
		if len(on) == 0 || start.IsZero() || (!mv.Open && start.After(now.Add(7*24*time.Hour))) {
			continue
		}
		title := mv.Title
		if title == "" {
			title = mv.Name
		}
		maints = append(maints, maint{Title: title, Start: start, End: end, Open: mv.Open, Monitors: on})
	}
	b, _ := json.Marshal(map[string]any{"title": sp.Title, "footer": sp.Footer, "theme": sp.Theme, "logo": sp.Logo,
		"windows": sp.Windows, "status": worst, "monitors": mons, "updated_at": now,
		"announcements": anns, "maintenance": maints, "subscribe": sp.Subscribe != "", "powered": !s.brandView().HidePowered, "support": s.supportText()})
	return b
}

func (s *Server) badge(w http.ResponseWriter, r *http.Request) {
	name, kind := r.PathValue("monitor"), strings.TrimSuffix(r.PathValue("kind"), ".svg")
	var row *rowJSON
	for _, sp := range s.app.EffectiveConfig().StatusPages {
		for _, rj := range s.published(sp) {
			if rj.Name == name {
				rj := rj
				row = &rj
			}
		}
	}
	if row == nil {
		writeError(w, notFound("badge"))
		return
	}
	label, value, color := row.Title, row.Status, BadgeColor(row.Status)
	switch kind {
	case "status":
	case "uptime":
		label += " 30d"
		u, _ := s.app.Store().UptimeOf(name, s.now().Add(-30*24*time.Hour), s.now())
		value, color = "n/a", BadgeColor("unknown")
		if r := s.shownRatio(u); r != nil {
			value = strconv.FormatFloat(*r*100, 'f', 2, 64) + "%"
			color = BadgeColor("ok")
			if *r < 0.99 {
				color = BadgeColor("degraded")
			}
			if *r < 0.95 {
				color = BadgeColor("critical")
			}
		}
	case "latency":
		value, color = fmt.Sprintf("%.0f ms", row.LatencyMS), BadgeColor("ok")
	default:
		writeError(w, notFound("badge"))
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = w.Write(RenderBadge(label, value, color))
}

// pushHandler is compatible with Uptime Kuma push URLs:
// /api/push/<token>?status=up|down&msg=...&ping=...
func (s *Server) pushHandler(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if ok, wait := s.push.Allow(tok, s.now()); !ok {
		writeError(w, rateLimited(wait))
		return
	}
	name, err := s.app.Store().MonitorByPushToken(tok)
	if errors.Is(err, store.ErrNotFound) || name == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "monitor not found or not active"})
		return
	}
	if err != nil || !s.app.IsPushReceiver(name) {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	up := q.Get("status") != "down"
	ping, _ := strconv.ParseFloat(q.Get("ping"), 64)
	msg := q.Get("msg")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if !s.app.Engine().Push(name, up, msg, time.Duration(ping*float64(time.Millisecond)), s.ip(r)) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "monitor not found or not active"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
