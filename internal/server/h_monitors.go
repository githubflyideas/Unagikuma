package server

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/observation"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// rowJSON is a monitor row of the API.
type rowJSON struct {
	Observations *observation.Snapshot `json:"observations,omitempty"`
	// ID is stable across renames (the name is what monitors.conf uses).
	ID          int64              `json:"id"`
	ETag        string             `json:"etag,omitempty"` // send back in If-Match when changing the record
	Name        string             `json:"name"`
	Title       string             `json:"title"`
	Type        string             `json:"type"`
	Written     string             `json:"written_type"`
	Target      string             `json:"target"`
	Keyword     string             `json:"keyword,omitempty"`
	Depth       string             `json:"depth"`
	Scope       string             `json:"scope"`
	Group       string             `json:"group,omitempty"`
	Tags        []tagJSON          `json:"tags"`
	Policy      string             `json:"policy"`
	IntervalS   int                `json:"interval_s"`
	Status      string             `json:"status"`
	StateType   string             `json:"state_type"`
	Flags       []string           `json:"flags"`
	Reason      string             `json:"reason,omitempty"`
	Detail      string             `json:"detail,omitempty"`
	Unknown     string             `json:"unknown_reason,omitempty"`
	Attempt     int                `json:"attempt"`
	MaxAttempts int                `json:"max_attempts"`
	LatencyMS   float64            `json:"latency_ms"`
	Uptime24h   *float64           `json:"uptime_24h"` // null while less than uptime.min-coverage of the day was watched
	Coverage24h float64            `json:"coverage_24h"`
	LastCheck   *time.Time         `json:"last_check"`
	LastChange  *time.Time         `json:"last_change"`
	NextCheck   *time.Time         `json:"next_check"`
	Incident    *incidentBrief     `json:"incident"`
	Disabled    bool               `json:"disabled"`
	Metrics     map[string]float64 `json:"metrics,omitempty"`
	// Area is the $SCOPE section; Nodes the nodes that check it (empty =
	// all).
	Area    string   `json:"area,omitempty"`
	Nodes   []string `json:"nodes,omitempty"`
	Parents []string `json:"parents,omitempty"`
	Host    string   `json:"host"`           // the host it is listed under
	Team    string   `json:"team,omitempty"` // the team that owns it (admin when none is written)
	Region  string   `json:"region,omitempty"`
	Mute    bool     `json:"mute,omitempty"`
	SysChan bool     `json:"system_channel,omitempty"`
	// Votes and Quorum appear only for monitors judged by several nodes.
	Votes  []engine.VoteView `json:"votes,omitempty"`
	Quorum *quorumJSON       `json:"quorum,omitempty"`
}

// quorumJSON is how the last round was decided.
type quorumJSON struct {
	Verdict  string   `json:"verdict"`
	Fail     int      `json:"fail"`
	OK       int      `json:"ok"`
	N        int      `json:"n"`
	Healthy  int      `json:"healthy"`
	Q        int      `json:"q"`
	QEff     int      `json:"q_eff"`
	Degraded bool     `json:"degraded"`
	Unknown  string   `json:"unknown_reason,omitempty"`
	Failing  []string `json:"failing_nodes,omitempty"`
	Summary  string   `json:"summary"`
}

type tagJSON struct {
	K string `json:"k"`
	V string `json:"v,omitempty"`
}

type incidentBrief struct {
	ID            int64      `json:"id"`
	NotifyCount   int        `json:"notify_count"`
	NotifyMax     int        `json:"notify_max"`
	Capped        bool       `json:"capped"`
	Acked         bool       `json:"acked"`
	AckedBy       string     `json:"acked_by,omitempty"`
	SilencedUntil *time.Time `json:"silenced_until"`
	OpenedAt      time.Time  `json:"opened_at"`
}

func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Server) row(name string) (rowJSON, bool) {
	r, ok := s.app.Engine().Row(name)
	if !ok {
		return rowJSON{}, false
	}
	return s.rowFrom(r, s.app.EffectiveConfig(), s.incidentIndex()), true
}

func (s *Server) incidentIndex() map[int64]engine.Incident {
	idx := map[int64]engine.Incident{}
	for _, inc := range s.app.Engine().OpenIncidents() {
		idx[inc.ID] = inc
	}
	return idx
}

func (s *Server) rowFrom(r engine.MonitorRow, cfg *config.Config, incs map[int64]engine.Incident) rowJSON {
	out := s.rowBase(r, cfg, incs)
	out.Observations = s.app.ObservationSnapshot(r.Name)
	if u, err := s.app.Store().UptimeOf(r.Name, s.now().Add(-24*time.Hour), s.now()); err == nil {
		out.Uptime24h, out.Coverage24h = s.shownRatio(u), u.Coverage
	}
	return out
}

// rowBase builds a row from memory only (no uptime, which needs the
// database), for filtering many rows cheaply.
func (s *Server) rowBase(r engine.MonitorRow, cfg *config.Config, incs map[int64]engine.Incident) rowJSON {
	out := rowJSON{
		ID: s.app.Store().MonitorID(r.Name), ETag: s.recordETags()[r.Name], Name: r.Name, Title: r.Title, Type: r.Type, Written: r.Type, Target: r.Target, Depth: r.Depth, Scope: r.Scope, Group: r.Group,
		Tags: []tagJSON{}, Status: r.Status, StateType: "soft", Flags: []string{}, Reason: r.Reason, Detail: r.Detail,
		Unknown: r.UnknownReason, Attempt: r.Attempt, MaxAttempts: r.Attempts, LatencyMS: r.LatencyMS,
		LastCheck: tp(r.LastCheck), LastChange: tp(r.LastChange), NextCheck: tp(r.NextCheck), Disabled: r.Paused, Metrics: r.Metrics,
	}
	if r.Hard {
		out.StateType = "hard"
	}
	if len(r.Votes) > 0 {
		out.Votes = r.Votes
	}
	if v := r.Quorum; v != nil && v.N > 1 {
		out.Quorum = &quorumJSON{Verdict: string(v.Kind), Fail: v.Fail, OK: v.OK, N: v.N, Healthy: v.Healthy, Q: v.Q, QEff: v.QEff,
			Degraded: v.Degraded, Unknown: v.UnknownReason, Failing: v.FailingNodes, Summary: v.Summary()}
	}
	if r.Partial {
		out.Flags = append(out.Flags, "partial")
	}
	if r.Depth == "L1" {
		out.Flags = append(out.Flags, "weak")
	}
	if r.Held {
		out.Flags = append(out.Flags, "held")
	}
	if m := cfg.FindMonitor(r.Name); m != nil {
		out.Policy, out.IntervalS, out.Written = m.Policy, int(m.Interval.Seconds()), m.Written
		out.Area, out.Nodes, out.Parents, out.Host, out.Team = m.Area, m.Nodes, m.Parents, m.HostKey(), m.TeamOf()
		out.Region, out.Mute, out.SysChan = m.Region, m.Mute, m.SystemChannel
		out.Target = m.Target
		if m.UDP != nil && m.UDP.Keyword != "" {
			out.Keyword = m.UDP.Keyword
			if m.Written == "UDP" {
				out.Target = m.UDP.Host + ":" + m.UDP.Keyword
			}
		}
		if m.Scope != "auto" || out.Scope == "" {
			if m.Scope != "auto" {
				out.Scope = m.Scope
			}
		}
		for _, t := range m.Tags {
			out.Tags = append(out.Tags, tagJSON{t.Key, t.Value})
		}
	}
	if inc, ok := incs[r.IncidentID]; ok && r.IncidentID != 0 {
		ib := &incidentBrief{ID: inc.ID, NotifyCount: inc.State.NotifyCount, Capped: inc.State.Capped, Acked: inc.State.Acked,
			AckedBy: inc.State.AckedBy, SilencedUntil: tp(inc.State.SilencedUntil), OpenedAt: inc.State.OpenedAt}
		if p := cfg.FindPolicy(inc.Policy); p != nil {
			ib.NotifyMax = p.Max
		}
		out.Incident = ib
	}
	return out
}

// monitors lists rows filtered by a simple query: words match name, title,
// target or group; key:value terms filter status, type, tag, group, depth,
// scope; a leading '-' negates a term.
func (s *Server) monitors(w http.ResponseWriter, r *http.Request) (any, error) {
	cfg := s.app.EffectiveConfig()
	incs := s.incidentIndex()
	q := parseQuery(r.URL.Query().Get("q"))
	var out []rowJSON
	for _, row := range s.app.Engine().Rows() {
		if !seesMonitor(r, row.Name) {
			continue
		}
		rj := s.rowFrom(row, cfg, incs)
		if q.match(rj) {
			out = append(out, rj)
		}
	}
	if out == nil {
		out = []rowJSON{}
	}
	qv := r.URL.Query()
	if qv.Get("limit") == "" && qv.Get("cursor") == "" {
		sortRows(out, qv.Get("sort"), qv.Get("order") == "desc")
		return map[string]any{"items": out, "total": len(out), "next_cursor": nil}, nil
	}
	// Pages are in id order, which neither renames nor status changes
	// reshuffle: walking them with next_cursor sees every monitor that
	// existed throughout once.
	if qv.Get("sort") != "" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "sort cannot be combined with limit or cursor; pages are in id order")
	}
	limit, err := strconv.Atoi(qv.Get("limit"))
	if qv.Get("limit") == "" {
		limit = 100
	} else if err != nil || limit < 1 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "limit must be a positive integer")
	}
	limit = min(limit, s.app.EffectiveConfig().Tunables.Int("api.page-max"))
	var after int64
	if c := qv.Get("cursor"); c != "" {
		if after, err = strconv.ParseInt(c, 10, 64); err != nil || after < 0 {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "invalid cursor")
		}
	}
	total := len(out)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	start := sort.Search(len(out), func(i int) bool { return out[i].ID > after })
	page := out[start:min(len(out), start+limit)]
	var next any
	if start+limit < len(out) {
		next = strconv.FormatInt(page[len(page)-1].ID, 10)
	}
	return map[string]any{"items": page, "total": total, "next_cursor": next}, nil
}

var statusRank = map[string]int{"critical": 0, "unknown": 1, "unreachable": 2, "degraded": 3, "pending": 4, "ok": 5, "maintenance": 6, "paused": 7}

func sortRows(rows []rowJSON, by string, desc bool) {
	less := func(a, b rowJSON) bool {
		switch by {
		case "name":
			return a.Name < b.Name
		case "last_change":
			return ptime(a.LastChange).After(ptime(b.LastChange))
		case "latency":
			return a.LatencyMS > b.LatencyMS
		case "uptime":
			return pf(a.Uptime24h) < pf(b.Uptime24h)
		case "interval":
			return a.IntervalS < b.IntervalS
		}
		if statusRank[a.Status] != statusRank[b.Status] {
			return statusRank[a.Status] < statusRank[b.Status]
		}
		return a.Name < b.Name
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if desc {
			return less(rows[j], rows[i])
		}
		return less(rows[i], rows[j])
	})
}

func ptime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func pf(f *float64) float64 {
	if f == nil {
		return 2
	}
	return *f
}

type queryTerm struct {
	key, val string
	neg      bool
}

type query []queryTerm

func parseQuery(s string) query {
	var q query
	for _, f := range strings.Fields(strings.ToLower(s)) {
		t := queryTerm{}
		if strings.HasPrefix(f, "-") && len(f) > 1 {
			t.neg, f = true, f[1:]
		}
		if k, v, ok := strings.Cut(f, ":"); ok && v != "" {
			switch k {
			case "status", "type", "tag", "group", "depth", "scope", "policy", "name", "flag", "area", "node", "host", "team", "region":
				t.key, t.val = k, v
				q = append(q, t)
				continue
			}
		}
		t.val = f
		q = append(q, t)
	}
	return q
}

func (q query) match(r rowJSON) bool {
	for _, t := range q {
		if t.match(r) == t.neg {
			return false
		}
	}
	return true
}

func (t queryTerm) match(r rowJSON) bool {
	low := strings.ToLower
	switch t.key {
	case "status":
		return r.Status == t.val
	case "type":
		return low(r.Type) == t.val || low(r.Written) == t.val
	case "depth":
		return low(r.Depth) == t.val
	case "scope":
		return r.Scope == t.val
	case "policy":
		return low(r.Policy) == t.val
	case "name":
		return strings.Contains(low(r.Name), t.val)
	case "group":
		return strings.Contains(low(r.Group), t.val)
	case "flag":
		for _, f := range r.Flags {
			if f == t.val {
				return true
			}
		}
		return false
	case "area":
		return strings.Contains(low(r.Area), t.val)
	case "host":
		return low(r.Host) == t.val
	case "team":
		return low(r.Team) == t.val
	case "region":
		return low(r.Region) == t.val
	case "node":
		if len(r.Nodes) == 0 {
			return true // checked by every node
		}
		for _, n := range r.Nodes {
			if low(n) == t.val {
				return true
			}
		}
		return false
	case "tag":
		k, v, hasV := strings.Cut(t.val, ":")
		for _, tg := range r.Tags {
			if low(tg.K) == k && (!hasV || low(tg.V) == v) {
				return true
			}
		}
		return false
	}
	hay := low(r.Name + " " + r.Title + " " + r.Target + " " + r.Group + " " + r.Keyword)
	return strings.Contains(hay, t.val)
}

var ranges = map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour}

func rangeOf(r *http.Request) (string, time.Duration) {
	key := r.URL.Query().Get("range")
	if d, ok := ranges[key]; ok {
		return key, d
	}
	return "24h", 24 * time.Hour
}

func (s *Server) heartbeats(w http.ResponseWriter, r *http.Request) (any, error) {
	key, d := rangeOf(r)
	cells := min(max(qInt(r, "cells", 60), 10), 200)
	names := strings.Split(r.URL.Query().Get("names"), ",")
	if r.URL.Query().Get("names") == "" {
		names = nil
		for _, row := range s.app.Engine().Rows() {
			names = append(names, row.Name)
		}
	}
	if len(names) > 500 {
		names = names[:500]
	}
	now := s.now()
	items := map[string][]store.Cell{}
	for _, n := range names {
		if !seesMonitor(r, n) {
			continue
		}
		c, err := s.app.Store().Cells(n, now.Add(-d), now, cells, s.historySite(r))
		if err != nil {
			return nil, err
		}
		items[n] = c
	}
	return map[string]any{"range": key, "cells": cells, "items": items}, nil
}

func (s *Server) monitorDetail(w http.ResponseWriter, r *http.Request) (any, error) {
	name := s.monitorParam(r)
	row, ok := s.row(name)
	if !ok || !seesMonitor(r, name) {
		return nil, notFound("monitor")
	}
	cur := s.app.EffectiveConfigState()
	m := cur.Config.FindMonitor(name)
	out := map[string]any{"row": row}
	// The path from this host to the target, for the diagram: the gateway
	// for every check, the internet exit only for internet targets.
	h := s.app.Engine().Health()
	path := map[string]any{"gateway": map[string]any{"addr": h.Gateway, "ok": h.GatewayOK, "state": h.GatewayState, "rtt_ms": float64(h.GatewayRTT) / 1e6, "note": h.GatewayNote}}
	if row.Scope == "wan" {
		path["exit"] = exitJSON(h)
	}
	out["path"] = path
	// The state of what it depends on, also for a team member who may not
	// open those monitors: it explains why this one is unreachable.
	type parentState struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Open   bool   `json:"open"`
	}
	ps := []parentState{}
	for _, p := range row.Parents {
		if pr, ok := s.app.Engine().Row(p); ok {
			ps = append(ps, parentState{Name: p, Status: string(pr.Status), Open: seesMonitor(r, p)})
		}
	}
	out["parent_states"] = ps
	if m != nil {
		lines := strings.Split(cur.Text, "\n")
		text := strings.Join(lines[m.Lines[0]-1:m.Lines[1]], "\n")
		opts := map[string]string{}
		for k, v := range m.Opts {
			opts[k] = v
		}
		out["opts"] = opts
		out["record"] = map[string]any{"text": text, "line": m.Lines[0], "file": "monitors.conf"}
		out["description"] = m.Description
		out["timeout_s"] = m.Timeout.Seconds()
		out["retry_s"] = m.Retry.Seconds()
		if m.Type == "PUSH" {
			out["push"] = map[string]any{"period_s": m.Period.Seconds(), "grace_s": m.Grace.Seconds()}
		}
	}
	now := s.now()
	up := map[string]*float64{}
	cov := map[string]float64{}
	partial := map[string]*float64{}
	var down float64
	for _, k := range []string{"24h", "7d", "30d", "90d"} {
		u, err := s.app.Store().UptimeOf(name, now.Add(-ranges[k]), now)
		if err != nil {
			return nil, err
		}
		up[k], cov[k] = s.shownRatio(u), u.Coverage
		if up[k] == nil && u.Ratio != nil {
			partial[k] = u.Ratio
		}
		if k == "30d" {
			down = u.DownS
		}
	}
	if site := s.historySite(r); site != "" {
		for k := range up {
			up[k] = nil
			cov[k] = 0
		}
		partial = map[string]*float64{}
		down = 0
		out["history_site"] = site
	}
	out["uptime"] = up
	out["coverage"] = cov
	out["uptime_partial"] = partial // the figure so far, for windows watched too briefly
	out["downtime_30d_s"] = down
	ev, _ := s.app.Store().EvidenceOf(name, 5, s.historySite(r))
	type evRow struct {
		Site        string          `json:"site,omitempty"`
		SourceEpoch string          `json:"source_epoch,omitempty"`
		OriginalID  int64           `json:"original_id"`
		At          time.Time       `json:"at"`
		Node        string          `json:"node"`
		Result      probeResultJSON `json:"result"`
	}
	evs := make([]evRow, 0, len(ev))
	for _, e := range ev {
		evs = append(evs, evRow{Site: e.Site, SourceEpoch: e.SourceEpoch, OriginalID: e.OriginalID, At: e.At, Node: e.Node, Result: resultJSON(e.Result)})
	}
	out["evidence"] = evs
	incs, _ := s.app.Store().Incidents("all", name, 0, 10, s.historySite(r))
	out["incidents"] = incs.Items
	return out, nil
}

func (s *Server) monitorLatency(w http.ResponseWriter, r *http.Request) (any, error) {
	if !seesMonitor(r, s.monitorParam(r)) {
		return nil, notFound("monitor")
	}
	_, d := rangeOf(r)
	pts, err := s.app.Store().Latency(s.monitorParam(r), s.now().Add(-d), s.now(), s.historySite(r))
	if err != nil {
		return nil, err
	}
	return map[string]any{"points": pts, "metric": "latency_ms"}, nil
}

func (s *Server) monitorTimeline(w http.ResponseWriter, r *http.Request) (any, error) {
	if !seesMonitor(r, s.monitorParam(r)) {
		return nil, notFound("monitor")
	}
	return s.app.Store().Events(store.EventFilter{Site: s.historySite(r), Monitor: s.monitorParam(r), Before: qInt64(r, "before"), Limit: qInt(r, "limit", 50)})
}

func (s *Server) monitorPush(w http.ResponseWriter, r *http.Request) (any, error) {
	name := s.monitorParam(r)
	m := s.app.EffectiveConfig().FindMonitor(name)
	if m == nil || m.Type != "PUSH" || !s.app.IsPushReceiver(name) || !seesMonitor(r, name) {
		return nil, notFound("push monitor")
	}
	tok, err := s.app.Store().PushToken(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": tok, "path": "/api/push/" + tok}, nil
}

func (s *Server) monitorPushRotate(w http.ResponseWriter, r *http.Request) (any, error) {
	name := s.monitorParam(r)
	if m := s.app.EffectiveConfig().FindMonitor(name); m == nil || m.Type != "PUSH" || !s.app.IsPushReceiver(name) || !seesMonitor(r, name) {
		return nil, notFound("push monitor")
	}
	if !s.allowsMonitor(authOf(r), name) {
		return nil, errOutsideAreas
	}
	tok, err := s.app.Store().RotatePushToken(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": tok, "path": "/api/push/" + tok}, nil
}

// recordReq is a monitor record from the UI form.
type recordReq struct {
	Record struct {
		Name     string            `json:"name"`
		Type     string            `json:"type"`
		Target   string            `json:"target"`
		Interval string            `json:"interval"`
		Opts     map[string]string `json:"opts"`
		// Area is the $SCOPE section ("" = global); nil leaves an
		// existing record in its section.
		Area *string `json:"area"`
	} `json:"record"`
	Message string `json:"message"`
}

func (rq recordReq) spec() config.RecordSpec {
	keys := make([]string, 0, len(rq.Record.Opts))
	for k := range rq.Record.Opts {
		keys = append(keys, k)
	}
	// title first, then alphabetical: stable, readable records.
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == "title") != (keys[j] == "title") {
			return keys[i] == "title"
		}
		return keys[i] < keys[j]
	})
	var opts []config.Opt
	for _, k := range keys {
		if v := rq.Record.Opts[k]; v != "" {
			opts = append(opts, config.Opt{Key: strings.ToLower(k), Value: v})
		}
	}
	spec := config.RecordSpec{Name: rq.Record.Name, Interval: rq.Record.Interval, Type: strings.ToUpper(rq.Record.Type), Target: rq.Record.Target, Opts: opts}
	if rq.Record.Area != nil {
		spec.Area, spec.MoveArea = *rq.Record.Area, true
	}
	return spec
}

func (s *Server) configError(err error) error {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	if ie, ok := app.IsInvalid(err); ok {
		ae := Errorf(http.StatusBadRequest, CodeInvalid, "%s", ie.Error())
		ae.Extra = map[string]any{"diagnostics": ie.Diags}
		if len(ie.Diags) > 0 {
			ae.Location = &Location{File: "monitors.conf", Line: ie.Diags[0].Pos.Line, Col: ie.Diags[0].Pos.Col}
		}
		return ae
	}
	var pe errPrecondition
	switch {
	case errors.As(err, &pe):
		e := &APIError{Status: http.StatusPreconditionFailed, Code: CodeChanged, Message: pe.Error()}
		if pe.current != "" {
			e.Extra = map[string]any{"record_etag": pe.current}
		} else {
			e.Status, e.Code, e.Message = http.StatusNotFound, CodeNotFound, "the record no longer exists"
		}
		return e
	case errors.Is(err, app.ErrConflict):
		return &APIError{Status: http.StatusPreconditionFailed, Code: CodeConflict, Message: err.Error(), Extra: map[string]any{"etag": s.app.Config().ETag()}}
	case errors.Is(err, app.ErrSuperseded), errors.Is(err, app.ErrMember):
		return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: err.Error()}
	case errors.Is(err, config.ErrNameTaken):
		return &APIError{Status: http.StatusConflict, Code: CodeConflict, Message: err.Error()}
	case errors.Is(err, app.ErrReadonly):
		return &APIError{Status: http.StatusConflict, Code: CodeConfigReadonly, Message: err.Error()}
	}
	return Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
}

func (s *Server) saved(st app.ConfigState) map[string]any {
	return map[string]any{"serial": st.Serial, "etag": st.ETag()}
}

func (s *Server) monitorCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req recordReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	spec := req.spec()
	if spec.Name == "" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "name is required")
	}
	if ai := authOf(r); len(ai.areas) > 0 {
		if req.Record.Area == nil {
			spec.Area = ai.areas[0] // into the caller's own section
		}
		if !ai.allowsArea(spec.Area) {
			return nil, errOutsideAreas
		}
	}
	if err := teamOfNewRecord(authOf(r), &spec); err != nil {
		return nil, err
	}
	if s.app.EffectiveConfig().HasName(spec.Name) {
		return nil, Errorf(http.StatusConflict, CodeConflict, "%s already exists", spec.Name)
	}
	if !config.IsMonitorType(spec.Type) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "unknown type %s", spec.Type)
	}
	if strings.EqualFold(spec.Type, "EXEC") {
		// Scripts are copied to each node by hand: a new script check
		// starts paused and is enabled once they are in place.
		var opts []config.Opt
		for _, o := range spec.Opts {
			if o.Key != "disabled" {
				opts = append(opts, o)
			}
		}
		spec.Opts = append(opts, config.Opt{Key: "disabled", Value: "yes"})
	}
	st, err := s.editConfig(r, req.Message, s.guardExec(authOf(r), func(f *config.File) error { return f.UpsertRecord(spec) }))
	if err != nil {
		return nil, s.configError(err)
	}
	a := auditOf(r).note("type", spec.Type).note("check", spec.Target)
	a.target, a.area = spec.Name, spec.Area
	out := s.savedRecord(st, spec.Name)
	out["name"] = spec.Name
	out["id"] = s.app.Store().MonitorID(spec.Name)
	return out, nil
}

func (s *Server) monitorUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	name := s.monitorParam(r)
	var req recordReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if s.app.EffectiveConfig().FindMonitor(name) == nil || !seesMonitor(r, name) {
		return nil, notFound("monitor")
	}
	spec := req.spec()
	if spec.Name == "" {
		spec.Name = name
	}
	if ai := authOf(r); !s.allowsMonitor(ai, name) || (spec.MoveArea && !ai.allowsArea(spec.Area)) {
		return nil, errOutsideAreas
	}
	if err := teamOfNewRecord(authOf(r), &spec); err != nil {
		return nil, err
	}
	st, err := s.editConfig(r, req.Message, s.guardExec(authOf(r), ifMatch(r, name, func(f *config.File) error {
		if spec.Name != name {
			if err := f.RenameRecord(name, spec.Name); err != nil {
				return err
			}
		}
		return f.UpsertRecord(spec)
	})))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.savedRecord(st, spec.Name), nil
}

func (s *Server) monitorDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	name := s.monitorParam(r)
	if s.app.EffectiveConfig().FindMonitor(name) == nil || !seesMonitor(r, name) {
		return nil, notFound("monitor")
	}
	if !s.allowsMonitor(authOf(r), name) {
		return nil, errOutsideAreas
	}
	st, err := s.editConfig(r, "delete "+name, ifMatch(r, name, func(f *config.File) error { return f.DeleteRecord(name) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

// monitorBulkDelete deletes several monitors in one configuration change,
// so a selection in the list costs one new serial, not one per monitor.
// With etags, a monitor someone else changed meanwhile fails the whole
// request and nothing is deleted.
func (s *Server) monitorBulkDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Names []string          `json:"names"`
		ETags map[string]string `json:"etags"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if len(req.Names) == 0 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no monitors given")
	}
	cfg := s.app.EffectiveConfig()
	for _, name := range req.Names {
		if cfg.FindMonitor(name) == nil || !seesMonitor(r, name) {
			return nil, notFound("monitor")
		}
		if !s.allowsMonitor(authOf(r), name) {
			return nil, errOutsideAreas
		}
	}
	msg := "delete " + strings.Join(req.Names, ", ")
	auditOf(r).target = strings.Join(req.Names, ", ")
	st, err := s.editConfig(r, msg, func(f *config.File) error {
		for _, name := range req.Names {
			if want := req.ETags[name]; want != "" {
				if cur := f.RecordETag(name); cur == "" || !etagListHas(want, cur) {
					return errPrecondition{cur}
				}
			}
			if err := f.DeleteRecord(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

// monitorBulkPause pauses or resumes several monitors on this site only.
// Monitors already in the wanted local state are left alone; when resuming,
// a script check whose script is missing on some node is skipped unless
// force is set, and its name is returned in "skipped".
func (s *Server) monitorBulkPause(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Names  []string          `json:"names"`
		Paused bool              `json:"paused"`
		Force  bool              `json:"force"`
		ETags  map[string]string `json:"etags"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if len(req.Names) == 0 {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no monitors given")
	}
	cfg := s.app.EffectiveConfig()
	etags := s.recordETags()
	var names, skipped []string
	seen := map[string]bool{}
	for _, name := range req.Names {
		m := cfg.FindMonitor(name)
		if m == nil || !seesMonitor(r, name) {
			return nil, notFound("monitor")
		}
		if !s.allowsMonitor(authOf(r), name) {
			return nil, errOutsideAreas
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if want := req.ETags[name]; want != "" {
			cur := etags[name]
			if cur == "" || !etagListHas(want, cur) {
				return nil, &APIError{Status: http.StatusPreconditionFailed, Code: CodeConflict, Message: errPrecondition{cur}.Error(), Extra: map[string]any{"etag": cur}}
			}
		}
		if s.app.EffectivelyPaused(name) == req.Paused {
			continue
		}
		if !req.Paused && m.Type == "EXEC" && !req.Force && len(s.scriptProblems(name)) > 0 {
			skipped = append(skipped, name)
			continue
		}
		names = append(names, name)
	}
	if skipped == nil {
		skipped = []string{}
	}
	if len(names) == 0 {
		return map[string]any{"changed": 0, "skipped": skipped, "site_local": true}, nil
	}
	auditOf(r).target = strings.Join(names, ", ")
	changed, err := s.app.SetLocalPaused(names, req.Paused, authOf(r).user)
	if err != nil {
		if errors.Is(err, engine.ErrNotFound) {
			return nil, notFound("monitor")
		}
		return nil, err
	}
	return map[string]any{"changed": changed, "skipped": skipped, "site_local": true}, nil
}

// monitorPause pauses or resumes one monitor on this site only.
func (s *Server) monitorPause(w http.ResponseWriter, r *http.Request) (any, error) {
	name := s.monitorParam(r)
	if !seesMonitor(r, name) {
		return nil, notFound("monitor")
	}
	if !s.allowsMonitor(authOf(r), name) {
		return nil, errOutsideAreas
	}
	var req struct {
		Paused bool `json:"paused"`
		Force  bool `json:"force"` // enable although some nodes lack the script
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	cfg := s.app.EffectiveConfig()
	m := cfg.FindMonitor(name)
	if m == nil {
		return nil, notFound("monitor")
	}
	if want := strings.TrimSpace(r.Header.Get("If-Match")); want != "" && want != "*" {
		cur := s.recordETags()[name]
		if cur == "" || !etagListHas(want, cur) {
			return nil, &APIError{Status: http.StatusPreconditionFailed, Code: CodeConflict, Message: errPrecondition{cur}.Error(), Extra: map[string]any{"etag": cur}}
		}
	}
	if m.Type == "EXEC" && !req.Paused && !req.Force {
		if bad := s.scriptProblems(name); len(bad) > 0 {
			e := Errorf(http.StatusConflict, CodeScriptsMissing, "the script of %s is missing or not executable on %d node(s)", name, len(bad))
			e.Extra = map[string]any{"nodes": bad, "plugin": probeName(m.Target, m.Opts)}
			return nil, e
		}
	}
	auditOf(r).target = name
	changed, err := s.app.SetLocalPaused([]string{name}, req.Paused, authOf(r).user)
	if err != nil {
		if errors.Is(err, engine.ErrNotFound) {
			return nil, notFound("monitor")
		}
		return nil, err
	}
	return map[string]any{
		"name":       name,
		"paused":     s.app.EffectivelyPaused(name),
		"changed":    changed,
		"site_local": true,
		"etag":       s.recordETags()[name],
	}, nil
}

func (s *Server) monitorCheck(w http.ResponseWriter, r *http.Request) (any, error) {
	if !seesMonitor(r, s.monitorParam(r)) {
		return nil, notFound("monitor")
	}
	err := s.app.Engine().CheckNow(s.monitorParam(r))
	switch {
	case errors.Is(err, engine.ErrNotFound):
		return nil, notFound("monitor")
	case errors.Is(err, engine.ErrRateLimited):
		return nil, rateLimited(s.app.EffectiveConfig().Tunables.Duration("probe.check-now-rate"))
	case err != nil:
		return nil, err
	}
	w.WriteHeader(http.StatusAccepted)
	return map[string]any{"queued": true}, nil
}

// probeResultJSON is a probe result as the API shows it: latency in
// milliseconds and the exchanged packets in hex, for the packet viewer.
type probeResultJSON struct {
	Vote      string             `json:"vote"`
	Reason    string             `json:"reason"`
	Detail    string             `json:"detail"`
	LatencyMS float64            `json:"latency_ms"`
	Depth     string             `json:"depth"`
	Degraded  bool               `json:"degraded"`
	Metrics   map[string]float64 `json:"metrics,omitempty"`
	Evidence  *evidenceJSON      `json:"evidence,omitempty"`
}

type evidenceJSON struct {
	Peer        string        `json:"peer,omitempty"`
	RequestHex  string        `json:"request_hex,omitempty"`
	ResponseHex string        `json:"response_hex,omitempty"`
	Decoded     []probe.Field `json:"decoded,omitempty"`
}

func resultJSON(res probe.Result) probeResultJSON {
	out := probeResultJSON{Vote: string(res.Vote), Reason: res.Reason, Detail: res.Detail, LatencyMS: float64(res.Latency) / 1e6,
		Depth: string(res.Depth), Degraded: res.Degraded, Metrics: res.Metrics}
	if e := res.Evidence; e != nil {
		out.Evidence = &evidenceJSON{Peer: e.Peer, RequestHex: hex.EncodeToString(e.Request), ResponseHex: hex.EncodeToString(e.Response), Decoded: e.Decoded}
	}
	return out
}

// probeOnce runs a check from an unsaved record ("test now").
func (s *Server) probeOnce(w http.ResponseWriter, r *http.Request) (any, error) {
	var req recordReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	spec := req.spec()
	if spec.Name == "" {
		spec.Name = "probe-test"
	}
	cur := s.app.Config()
	text := strings.TrimRight(cur.Text, "\n") + "\n" + spec.Render() + "\n"
	if old := cur.Config.FindMonitor(spec.Name); old != nil {
		f, _ := config.Parse("monitors.conf", []byte(cur.Text))
		_ = f.UpsertRecord(spec)
		text = string(f.Bytes())
	}
	cfg, diags := s.app.ValidateConfig([]byte(text))
	if config.HasErrors(diags) {
		return nil, s.configError(&app.InvalidError{Diags: diags})
	}
	m := cfg.FindMonitor(spec.Name)
	if m == nil || m.Type == "PUSH" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "nothing to probe")
	}
	if ai := authOf(r); m.Type == "EXEC" && ai != nil && !ai.scopes["admin"] {
		return nil, errExecAdmin
	}
	ps, err := app.ProbeSpec(m, s.app.Resolver())
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	app.ApplyProbeDefaults(&ps, s.app.EffectiveConfig().Tunables)
	ctx, cancel := context.WithTimeout(r.Context(), ps.Timeout+5*time.Second)
	defer cancel()
	res := s.app.Engine().ProbeOnce(ctx, ps)
	return resultJSON(res), nil
}

// sameCheck tells the monitor form, while a monitor is being written,
// which monitors already check exactly the same thing (they could share
// one check) and how many others watch the same host. exclude is the
// monitor being edited.
func (s *Server) sameCheck(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		recordReq
		Exclude string `json:"exclude"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	type mon struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	}
	out := struct {
		Same []mon `json:"same"`
		Host int   `json:"host"`
	}{Same: []mon{}}
	cur := s.app.Config()
	spec := req.spec()
	spec.Name = "same-check-draft"
	for cur.Config.FindMonitor(spec.Name) != nil {
		spec.Name += "-"
	}
	text := strings.TrimRight(cur.Text, "\n") + "\n" + spec.Render() + "\n"
	cfg, diags := s.app.ValidateConfig([]byte(text))
	draft := cfg.FindMonitor(spec.Name)
	if draft == nil || config.HasErrors(diags) && draftHasErrors(diags, text) {
		return out, nil // still being written
	}
	key, host := draft.CheckKey(), draft.HostKey()
	for _, m := range cur.Config.Monitors {
		switch {
		case m.Name == req.Exclude || m.Disabled || !seesMonitor(r, m.Name):
		case key != "" && m.CheckKey() == key:
			out.Same = append(out.Same, mon{m.Name, m.Title})
		case host != "" && m.Type != "PUSH" && m.HostKey() == host:
			out.Host++
		}
	}
	return out, nil
}

// draftHasErrors reports errors on the record appended last.
func draftHasErrors(diags []config.Diagnostic, text string) bool {
	last := strings.Count(text, "\n")
	for _, d := range diags {
		if d.Severity == config.SevError && d.Pos.Line >= last {
			return true
		}
	}
	return false
}

// quickadd turns one-line shorthands ("udp 10.1.1.1:snmp", "https://x")
// into records.
func (s *Server) quickadd(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	type rec struct {
		Name     string            `json:"name"`
		Type     string            `json:"type"`
		Target   string            `json:"target"`
		Opts     map[string]string `json:"opts"`
		Text     string            `json:"text"`
		Warnings []string          `json:"warnings"`
	}
	type lineErr struct {
		Line int    `json:"line"`
		Msg  string `json:"msg"`
	}
	out := struct {
		Records []rec     `json:"records"`
		Errors  []lineErr `json:"errors"`
	}{Records: []rec{}, Errors: []lineErr{}}
	cur := s.app.Config()
	used := map[string]bool{}
	for _, m := range cur.Config.Monitors {
		used[m.Name] = true
	}
	for i, line := range strings.Split(req.Text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		spec, err := quickRecord(line, used)
		if err != nil {
			out.Errors = append(out.Errors, lineErr{i + 1, err.Error()})
			continue
		}
		used[spec.Name] = true
		text := strings.TrimRight(cur.Text, "\n") + "\n" + spec.Render() + "\n"
		_, diags := s.app.ValidateConfig([]byte(text))
		rc := rec{Name: spec.Name, Type: spec.Type, Target: spec.Target, Opts: map[string]string{}, Text: spec.Render(), Warnings: []string{}}
		for _, o := range spec.Opts {
			rc.Opts[o.Key] = o.Value
		}
		bad := false
		for _, d := range diags {
			if d.Pos.Line < strings.Count(text, "\n") {
				continue // diagnostics of the existing file
			}
			if d.Severity == config.SevError {
				out.Errors = append(out.Errors, lineErr{i + 1, d.Msg})
				bad = true
			} else if d.Code == "same_check" && teamOf(r) != nil {
				// It names a monitor the team member may not see.
				continue
			} else {
				rc.Warnings = append(rc.Warnings, d.Msg)
			}
		}
		if !bad {
			out.Records = append(out.Records, rc)
		}
	}
	return out, nil
}

// CodeScriptsMissing refuses to enable a script check some nodes cannot run.
const CodeScriptsMissing = "scripts_missing"

func probeName(target string, o map[string]string) string { return probe.PluginOf(target, o) }

// shownRatio is the uptime to show for a window: nil (shown as —) while
// the monitor was watched for less than uptime.min-coverage of it, so a
// monitor added an hour ago does not claim a 30-day figure.
func (s *Server) shownRatio(u store.Uptime) *float64 {
	if u.Coverage < s.app.EffectiveConfig().Tunables.Float("uptime.min-coverage") {
		return nil
	}
	return u.Ratio
}
