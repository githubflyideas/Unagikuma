package server

import (
	"errors"
	"github.com/githubflyideas/unagikuma/internal/app"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// ---- overview ----

// pathView is one probe node's way out, for the overview diagram: its
// gateway, its uplink as the local network detector judges it, and the
// internet exit it pings.
type pathView struct {
	Name    string         `json:"name"`
	Title   string         `json:"title,omitempty"`
	Region  string         `json:"region,omitempty"`
	Self    bool           `json:"self,omitempty"`
	Online  bool           `json:"online"`
	Gateway map[string]any `json:"gateway"`
	Uplink  string         `json:"uplink"`
	Exit    map[string]any `json:"exit"`
}

func pathOf(n nodeView, h *engine.Health) pathView {
	p := pathView{Name: n.Name, Title: n.Title, Region: n.Region, Self: n.Self, Online: n.Online, Uplink: "unknown"}
	if h != nil && n.Online {
		p.Gateway = map[string]any{"addr": h.Gateway, "ok": h.GatewayOK, "state": h.GatewayState, "rtt_ms": float64(h.GatewayRTT) / 1e6, "note": h.GatewayNote}
		p.Uplink = h.Uplink
		p.Exit = exitJSON(*h)
	}
	return p
}

func exitJSON(h engine.Health) map[string]any {
	if h.Exit == "" {
		return nil
	}
	return map[string]any{"addr": h.Exit, "ok": h.ExitOK, "rtt_ms": float64(h.ExitRTT) / 1e6, "checked": tp(h.ExitAt), "idle": h.ExitIdle}
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) (any, error) {
	if ts := teamOf(r); ts != nil {
		return s.teamOverview(r), nil
	}
	ov := s.app.Engine().Overview()
	now := s.now()
	counts := map[string]int{"total": 0}
	for k, v := range ov.Counts {
		counts[k] = v
		counts["total"] += v
	}
	week, _ := s.app.Store().Counters(now.AddDate(0, 0, -7))
	hb := s.app.HeartbeatStatus()
	h := ov.Health
	health := map[string]any{
		"uplink":      map[string]any{"state": h.Uplink, "since": tp(h.UplinkSince), "suspected": h.Suspected, "wan_domains": h.WANDomains, "wan_anchors": h.WANAnchors},
		"gateway":     map[string]any{"addr": h.Gateway, "ok": h.GatewayOK, "state": h.GatewayState, "rtt_ms": float64(h.GatewayRTT) / 1e6, "note": h.GatewayNote},
		"exit":        exitJSON(h),
		"dns_down":    h.DNSDown,
		"heartbeat":   map[string]any{"configured": hb.Configured, "target": hb.Target, "last_ok": tp(hb.LastOK), "last_error": hb.LastError},
		"anomalies":   ov.Anomalies,
		"interrupted": nil,
	}
	if in := ov.LastInterruption; in != nil {
		health["interrupted"] = map[string]any{"at": in.Start, "kind": in.Kind, "duration_s": int(in.Duration.Seconds())}
	}
	// Channel health over 24h from the notification log.
	type ch struct {
		Name        string   `json:"name"`
		Kind        string   `json:"kind"`
		Sent24h     int      `json:"sent_24h"`
		Failed24h   int      `json:"failed_24h"`
		SuccessRate *float64 `json:"success_rate"`
		AvgMS       float64  `json:"avg_ms"`
		LastError   string   `json:"last_error,omitempty"`
	}
	cfg := s.app.EffectiveConfig()
	stats := map[string]*ch{}
	var chans []*ch
	for _, c := range cfg.Channels {
		x := &ch{Name: c.Name, Kind: c.Kind}
		stats[c.Name] = x
		chans = append(chans, x)
	}
	if p, err := s.app.Store().Deliveries("", 0, 1000); err == nil {
		lat := map[string]float64{}
		for _, d := range p.Items {
			if now.Sub(d.At) > 24*time.Hour {
				break
			}
			for _, a := range d.Attempts {
				x := stats[a.Channel]
				if x == nil {
					continue
				}
				x.Sent24h++
				lat[a.Channel] += float64(a.Latency) / 1e6
				if !a.OK {
					x.Failed24h++
					if x.LastError == "" {
						x.LastError = a.Error
					}
				}
			}
		}
		for name, x := range stats {
			if x.Sent24h > 0 {
				r := 1 - float64(x.Failed24h)/float64(x.Sent24h)
				x.SuccessRate, x.AvgMS = &r, lat[name]/float64(x.Sent24h)
			}
		}
	}
	if chans == nil {
		chans = []*ch{}
	}
	// Certificates expiring soon.
	type exp struct {
		Monitor string  `json:"monitor"`
		Title   string  `json:"title"`
		Days    float64 `json:"days"`
		What    string  `json:"what"`
	}
	expiring := []exp{}
	// Certificates show up inside the certificate warning window (62 days
	// by default); domain registrations inside 30.
	certWin := float64(cfg.Tunables.Int("probe.cert-warn-days"))
	if certWin <= 0 {
		certWin = 62
	}
	for _, row := range s.app.Engine().Rows() {
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
	// Problems reach someone: the admin team has channels, or there are
	// system channels (or, in a file not yet migrated, a default policy).
	alerting := false
	for _, name := range []string{config.TeamPolicy(config.AdminTeam), "system", "default"} {
		if p := cfg.FindPolicy(name); p != nil && len(p.Chain) > 0 {
			alerting = true
		}
	}
	// The node panel appears only once there are remote nodes: a single
	// machine shows no cluster furniture.
	views := s.nodeViews()
	rep := s.app.ReplicaStatus()
	layout, nodes := "single", []nodeView{}
	switch {
	case rep.Replica:
		layout, nodes = "replica", views
	case len(views) > 1:
		layout, nodes = "cluster", views
	}
	paths := []pathView{}
	for _, v := range views {
		if !v.Probe {
			continue
		}
		if v.Self {
			paths = append(paths, pathOf(v, &h))
		} else {
			paths = append(paths, pathOf(v, v.Health))
		}
	}
	return map[string]any{
		"instance": s.app.Instance(), "serial": ov.Serial, "layout": layout, "counts": counts, "unhandled": ov.Unhandled,
		"degraded": ov.Degraded, "value_week": week, "health": health, "channels": chans, "expiring": expiring,
		"open_system": ov.OpenSystem, "pending_notifications": ov.PendingIntents, "banner": s.app.Banner(),
		"alerting_configured": alerting, "nodes": nodes, "paths": paths, "tactical": s.tactical(r), "script_problems": s.scriptCount(),
		"superseded": s.app.SupersededBy(), "term": s.app.Term(), "contact_issues": len(s.app.ChannelIssues()),
		"people": len(s.app.Directory().People()), "status_pages": len(cfg.StatusPages), "storage": s.app.Store().WriteHealth(),
	}, nil
}

// ---- incidents ----

func (s *Server) incidents(w http.ResponseWriter, r *http.Request) (any, error) {
	state := r.URL.Query().Get("state")
	switch state {
	case "unhandled", "handled":
		now := s.now()
		var out []store.IncidentRow
		for _, inc := range s.app.Engine().OpenIncidents() {
			if !inc.State.Open() || inc.State.Drill {
				continue
			}
			if !seesMonitor(r, inc.Monitor) {
				continue
			}
			if engine.Unhandled(&inc, now) == (state == "unhandled") {
				row := store.IncidentRowOf(inc)
				row.Acks, _ = s.app.Store().Acks(inc.ID)
				out = append(out, row)
			}
		}
		if out == nil {
			out = []store.IncidentRow{}
		}
		return map[string]any{"items": out, "next_cursor": nil}, nil
	case "resolved", "all", "open":
	default:
		state = "all"
	}
	return s.app.Store().IncidentsOf(state, r.URL.Query().Get("monitor"), teamOf(r).list(), qInt64(r, "before"), qInt(r, "limit", 100), s.historySite(r))
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, notFound("incident")
	}
	return id, nil
}

func engineErr(err error) error {
	if errors.Is(err, app.ErrSourceUnavailable) {
		return Errorf(http.StatusServiceUnavailable, CodeConflict, "incident source unavailable")
	}
	if errors.Is(err, engine.ErrNotFound) {
		return notFound("incident")
	}
	return err
}

func (s *Server) incidentAck(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.incidentGate(r, id); err != nil {
		return nil, err
	}
	var req struct {
		Comment  string `json:"comment"`
		ExpiresS int    `json:"expires_s"`
	}
	_ = decodeJSON(r, s.maxBody(), &req)
	if err := s.app.ActOnIncident(r.Context(), id, app.IncidentAction{Action: "ack", Actor: authOf(r).user, Comment: req.Comment, DurationS: req.ExpiresS}, incidentVia(r)); err != nil {
		return nil, engineErr(err)
	}
	return nil, nil
}

func (s *Server) incidentUnack(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.incidentGate(r, id); err != nil {
		return nil, err
	}
	if err := s.app.ActOnIncident(r.Context(), id, app.IncidentAction{Action: "unack", Actor: authOf(r).user}, incidentVia(r)); err != nil {
		return nil, engineErr(err)
	}
	return nil, nil
}

func (s *Server) incidentSilence(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.incidentGate(r, id); err != nil {
		return nil, err
	}
	// Flat, or wrapped in "record" like the other write endpoints.
	var req struct {
		DurationS int `json:"duration_s"`
		Record    *struct {
			DurationS int `json:"duration_s"`
		} `json:"record"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if req.Record != nil {
		if req.DurationS != 0 && req.DurationS != req.Record.DurationS {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "duration_s is given twice with different values")
		}
		req.DurationS = req.Record.DurationS
	}
	return nil, engineErr(s.app.ActOnIncident(r.Context(), id, app.IncidentAction{Action: "silence", Actor: authOf(r).user, DurationS: req.DurationS}, incidentVia(r)))
}

func (s *Server) incidentBulk(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		IDs       []int64 `json:"ids"`
		Action    string  `json:"action"`
		Comment   string  `json:"comment"`
		DurationS int     `json:"duration_s"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	done := 0
	failed := map[int64]string{}
	for _, id := range req.IDs {
		if s.incidentGate(r, id) != nil {
			continue
		}
		if req.Action != "ack" && req.Action != "silence" {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "action must be ack or silence")
		}
		err := s.app.ActOnIncident(r.Context(), id, app.IncidentAction{Action: req.Action, Actor: authOf(r).user, Comment: req.Comment, DurationS: req.DurationS}, incidentVia(r))
		if err == nil {
			done++
		} else {
			failed[id] = engineErr(err).Error()
		}
	}
	return map[string]any{"done": done, "failed": failed}, nil
}

// ---- events and notifications ----

func (s *Server) events(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query()
	var types []string
	if t := q.Get("type"); t != "" {
		types = strings.Split(strings.ToUpper(t), ",")
	}
	return s.app.Store().Events(store.EventFilter{Site: s.historySite(r), Monitor: q.Get("monitor"), Only: teamOf(r).list(), Types: types, Severity: q.Get("severity"),
		Before: qInt64(r, "before"), Limit: qInt(r, "limit", 100)})
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.Store().Deliveries(r.URL.Query().Get("filter"), qInt64(r, "before"), qInt(r, "limit", 100), s.historySite(r))
}

// ---- configuration ----

func (s *Server) configGet(w http.ResponseWriter, r *http.Request) (any, error) {
	cur := s.app.EffectiveConfigState()
	mode := "ro"
	if s.app.ConfigAuthority() {
		mode = "rw"
	}
	_, diags := s.app.ValidateConfig([]byte(cur.Text))
	if diags == nil {
		diags = []config.Diagnostic{}
	}
	w.Header().Set("ETag", cur.ETag())
	return map[string]any{"serial": cur.Serial, "etag": cur.ETag(), "path": s.app.ConfigPath(), "mode": mode, "text": cur.Text,
		"diagnostics": diags, "banner": s.app.Banner()}, nil
}

func (s *Server) configPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Text    string `json:"text"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if err := s.execAllowed(authOf(r), []byte(req.Text)); err != nil {
		return nil, err
	}
	st, err := s.app.SaveConfigFrom([]byte(req.Text), r.Header.Get("If-Match"), authOf(r).user, req.Message, s.client(r, authOf(r)))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

func (s *Server) configValidate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	cfg, diags := s.app.ValidateConfig([]byte(req.Text))
	if diags == nil {
		diags = []config.Diagnostic{}
	}
	out := map[string]any{"diagnostics": diags, "diff": config.UnifiedDiff(s.app.Config().Text, req.Text)}
	if !config.HasErrors(diags) {
		changes := config.Diff(s.app.EffectiveConfig(), cfg)
		if changes == nil {
			changes = []config.Change{}
		}
		out["changes"] = changes
	}
	return out, nil
}

func (s *Server) configHistory(w http.ResponseWriter, r *http.Request) (any, error) {
	items, err := s.app.Store().Snapshots(qInt(r, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

func (s *Server) configDiff(w http.ResponseWriter, r *http.Request) (any, error) {
	get := func(k string) (string, error) {
		v := r.URL.Query().Get(k)
		if v == "" || v == "current" {
			return s.app.Config().Text, nil
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return "", Errorf(http.StatusBadRequest, CodeInvalid, "bad serial %q", v)
		}
		sn, err := s.app.Store().SnapshotText(uint32(n))
		if err != nil {
			return "", notFound("version")
		}
		return sn.Text, nil
	}
	a, err := get("from")
	if err != nil {
		return nil, err
	}
	b, err := get("to")
	if err != nil {
		return nil, err
	}
	return map[string]any{"unified": config.UnifiedDiff(a, b)}, nil
}

func (s *Server) configRollback(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Serial uint32 `json:"serial"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if sn, err := s.app.Store().SnapshotText(req.Serial); err == nil {
		if err := s.execAllowed(authOf(r), []byte(sn.Text)); err != nil {
			return nil, err
		}
	}
	st, err := s.app.Rollback(req.Serial, authOf(r).user, s.client(r, authOf(r)))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

func (s *Server) configReload(w http.ResponseWriter, r *http.Request) (any, error) {
	st, err := s.app.BumpSerial(authOf(r).user, s.client(r, authOf(r)))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

func incidentVia(r *http.Request) string {
	if ai := authOf(r); ai != nil && ai.session {
		return "web"
	}
	return "api"
}
func (s *Server) historySite(r *http.Request) string {
	site := r.URL.Query().Get("site")
	if site == s.app.SiteName() {
		return ""
	}
	return site
}
func (s *Server) historySites(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.Store().HistorySites()
}
