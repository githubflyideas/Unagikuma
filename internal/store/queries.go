package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

var gapFactorBits atomic.Uint64

func init() { gapFactorBits.Store(math.Float64bits(2.5)) }

// SetTunables applies the storage-related tunables.
func (s *Store) SetTunables(ts tunables.Set) {
	gapFactorBits.Store(math.Float64bits(ts.Float("storage.gap-factor")))
	r := RetentionFrom(ts)
	s.ret.Store(&r)
}

// retention is the history retention in effect.
func (s *Store) retention() Retention {
	if r := s.ret.Load(); r != nil {
		return *r
	}
	return Retention{Raw: 48 * time.Hour, Agg5m: 7 * 24 * time.Hour, Agg1h: 400 * 24 * time.Hour}
}

func (s *Store) gapFactor() float64 { return math.Float64frombits(gapFactorBits.Load()) }

// Page is a cursor page of rows (newest first).
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor *int64 `json:"next_cursor"`
}

// EventRow is a stored event.
type EventRow struct {
	Site     string         `json:"site,omitempty"`
	ID       int64          `json:"id"`
	At       time.Time      `json:"at"`
	Type     string         `json:"type"`
	Severity string         `json:"severity"`
	Monitor  string         `json:"monitor,omitempty"`
	Message  string         `json:"message"`
	Data     map[string]any `json:"data,omitempty"`
}

// EventFilter selects events.
type EventFilter struct {
	Site    string
	Monitor string
	// Only, when set, keeps the events of these monitors and nothing else
	// (an empty list keeps none): what a team member may see.
	Only     *[]string
	Types    []string
	Severity string
	Before   int64 // cursor: id < Before
	Limit    int
}

func limitOf(n int) int {
	if n <= 0 {
		return 100
	}
	return min(n, 1000)
}

// Events lists events, newest first.
func (s *Store) Events(f EventFilter) (Page[EventRow], error) {
	var where []string
	var args []any
	if f.Site != "*" {
		where = append(where, "site=?")
		args = append(args, f.Site)
	}
	if f.Monitor != "" {
		where, args = append(where, "monitor=?"), append(args, f.Monitor)
	}
	if f.Only != nil {
		w, a := inList("monitor", *f.Only)
		where, args = append(where, w), append(args, a...)
	}
	if len(f.Types) > 0 {
		where = append(where, "type IN (?"+strings.Repeat(",?", len(f.Types)-1)+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.Severity != "" {
		where, args = append(where, "severity=?"), append(args, f.Severity)
	}
	if f.Before > 0 {
		where, args = append(where, "id<?"), append(args, f.Before)
	}
	q := `SELECT id,at,type,severity,monitor,message,data,site FROM events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	lim := limitOf(f.Limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT %d", lim+1)
	rows, err := s.core.Query(q, args...)
	if err != nil {
		return Page[EventRow]{}, err
	}
	defer rows.Close()
	var out Page[EventRow]
	out.Items = []EventRow{}
	for rows.Next() {
		var e EventRow
		var at int64
		var sev, mon, msg, data sql.NullString
		if err := rows.Scan(&e.ID, &at, &e.Type, &sev, &mon, &msg, &data, &e.Site); err != nil {
			return out, err
		}
		e.At, e.Severity, e.Monitor, e.Message = fromMS(at), sev.String, mon.String, msg.String
		if data.Valid {
			_ = json.Unmarshal([]byte(data.String), &e.Data)
		}
		out.Items = append(out.Items, e)
	}
	if len(out.Items) > lim {
		out.Items = out.Items[:lim]
		c := out.Items[lim-1].ID
		out.NextCursor = &c
	}
	return out, rows.Err()
}

// DeliveryRow is a notification log entry.
type DeliveryRow struct {
	Site       string           `json:"site,omitempty"`
	ID         int64            `json:"id"`
	IntentID   string           `json:"intent_id"`
	IncidentID int64            `json:"incident_id"`
	Kind       string           `json:"kind"`
	Seq        int              `json:"seq"`
	At         time.Time        `json:"at"`
	Attempts   []notify.Attempt `json:"attempts"`
	OK         bool             `json:"ok"`
	Fallback   bool             `json:"fallback"`
	Drill      bool             `json:"drill"`
	DelayedS   int              `json:"delayed_s"`
}

// Deliveries lists the notification log; filter is "" | failed | fallback | drill.
func (s *Store) Deliveries(filter string, before int64, limit int, sites ...string) (Page[DeliveryRow], error) {
	var where []string
	var args []any
	site := selectedSite(sites)
	if site != "*" {
		where = append(where, "site=?")
		args = append(args, site)
	}
	switch filter {
	case "failed":
		where = append(where, "ok=0")
	case "fallback":
		where = append(where, "fallback=1")
	case "drill":
		where = append(where, "drill=1")
	}
	if before > 0 {
		where, args = append(where, "id<?"), append(args, before)
	}
	q := `SELECT id,intent_id,incident_id,kind,seq,at,attempts,ok,fallback,drill,delayed_s,site FROM deliveries`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	lim := limitOf(limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT %d", lim+1)
	rows, err := s.core.Query(q, args...)
	if err != nil {
		return Page[DeliveryRow]{}, err
	}
	defer rows.Close()
	out := Page[DeliveryRow]{Items: []DeliveryRow{}}
	for rows.Next() {
		var d DeliveryRow
		var at int64
		var att sql.NullString
		var inc sql.NullInt64
		var intent sql.NullString
		if err := rows.Scan(&d.ID, &intent, &inc, &d.Kind, &d.Seq, &at, &att, &d.OK, &d.Fallback, &d.Drill, &d.DelayedS, &d.Site); err != nil {
			return out, err
		}
		d.IntentID, d.IncidentID, d.At = intent.String, inc.Int64, fromMS(at)
		_ = json.Unmarshal([]byte(att.String), &d.Attempts)
		out.Items = append(out.Items, d)
	}
	if len(out.Items) > lim {
		out.Items = out.Items[:lim]
		c := out.Items[lim-1].ID
		out.NextCursor = &c
	}
	return out, rows.Err()
}

// IncidentRow is an incident for lists.
type IncidentRow struct {
	GlobalID    string               `json:"global_id"`
	SourceNode  string               `json:"source_node"`
	SourceEpoch string               `json:"source_epoch"`
	OriginalID  int64                `json:"original_id"`
	HistoryOnly bool                 `json:"history_only,omitempty"`
	ID          int64                `json:"id"`
	Monitor     string               `json:"monitor"`
	Kind        string               `json:"kind"`
	System      string               `json:"system,omitempty"`
	Policy      string               `json:"policy"`
	Title       string               `json:"title"`
	Reason      string               `json:"reason"`
	Detail      string               `json:"detail"`
	AckComment  string               `json:"ack_comment,omitempty"` // the latest; Acks has them all
	State       engine.IncidentState `json:"state"`
	Acks        []AckRecord          `json:"acks,omitempty"`
	Site        string               `json:"site,omitempty"` // merged from another site of the cluster
}

// incidentSites returns the site of the incidents among ids that were
// merged from another site.
func (s *Store) incidentSites(rows []IncidentRow) {
	if len(rows) == 0 {
		return
	}
	idx := map[int64]int{}
	ph := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows))
	for i, r := range rows {
		idx[r.ID] = i
		ph, args = append(ph, "?"), append(args, r.ID)
	}
	q, err := s.core.Query(`SELECT id,site FROM incidents WHERE site!='' AND id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return
	}
	defer q.Close()
	for q.Next() {
		var id int64
		var site string
		if q.Scan(&id, &site) == nil {
			rows[idx[id]].Site = site
			rows[idx[id]].HistoryOnly = true
		}
	}
}

// IncidentRowOf is the listing row of an incident.
func IncidentRowOf(inc engine.Incident) IncidentRow {
	return IncidentRow{HistoryOnly: inc.State.Closed && inc.State.ResolvedAt.IsZero(), GlobalID: inc.GlobalID, SourceNode: inc.SourceNode, SourceEpoch: inc.SourceEpoch, OriginalID: inc.OriginalID, ID: inc.ID, Monitor: inc.Monitor, Kind: inc.Kind, System: inc.System, Policy: inc.Policy, Title: inc.Title,
		Reason: inc.Reason, Detail: inc.Detail, AckComment: inc.AckComment, State: inc.State}
}

// AckRecord is one acknowledgement or its withdrawal.
type AckRecord struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	Via     string    `json:"via"`    // web | api
	Action  string    `json:"action"` // ack | unack
	Comment string    `json:"comment,omitempty"`
}

// AddAck appends to an incident's acknowledgement history.
func (s *Store) AddAck(incident int64, a AckRecord) error {
	_, err := s.core.Exec(`INSERT INTO acks(incident_id, at, by, via, action, comment) VALUES(?,?,?,?,?,?)`,
		incident, a.At.Unix(), a.By, a.Via, a.Action, a.Comment)
	return err
}

// Acks returns an incident's acknowledgement history, oldest first.
func (s *Store) Acks(incident int64) ([]AckRecord, error) {
	rows, err := s.core.Query(`SELECT at, by, via, action, comment FROM acks WHERE incident_id=? ORDER BY id`, incident)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AckRecord
	for rows.Next() {
		var a AckRecord
		var at int64
		if err := rows.Scan(&at, &a.By, &a.Via, &a.Action, &a.Comment); err != nil {
			return nil, err
		}
		a.At = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Incidents lists incidents: filter open | resolved | all; monitor optional.
func (s *Store) Incidents(filter, monitor string, before int64, limit int, sites ...string) (Page[IncidentRow], error) {
	return s.IncidentsOf(filter, monitor, nil, before, limit, sites...)
}

// inList is "col IN (?,...)" for vals; with no values it matches nothing.
func inList(col string, vals []string) (string, []any) {
	if len(vals) == 0 {
		return "0", nil
	}
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return col + " IN (?" + strings.Repeat(",?", len(vals)-1) + ")", args
}

// IncidentsOf is Incidents limited to the monitors in only when it is not
// nil (an empty list keeps none): what a team member may see.
func (s *Store) IncidentsOf(filter, monitor string, only *[]string, before int64, limit int, sites ...string) (Page[IncidentRow], error) {
	var where []string
	var args []any
	site := selectedSite(sites)
	if site != "*" {
		where = append(where, "site=?")
		args = append(args, site)
	}
	if only != nil {
		w, a := inList("monitor", *only)
		where, args = append(where, w), append(args, a...)
	}
	switch filter {
	case "open":
		where = append(where, "resolved_at=0 AND closed=0")
	case "resolved":
		where = append(where, "resolved_at>0")
	}
	if monitor != "" {
		where, args = append(where, "monitor=?"), append(args, monitor)
	}
	if before > 0 {
		where, args = append(where, "id<?"), append(args, before)
	}
	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ")
	}
	lim := limitOf(limit)
	incs, err := s.queryIncidents(w+fmt.Sprintf(" ORDER BY id DESC LIMIT %d", lim+1), args)
	if err != nil {
		return Page[IncidentRow]{}, err
	}
	out := Page[IncidentRow]{Items: []IncidentRow{}}
	for _, inc := range incs {
		out.Items = append(out.Items, IncidentRowOf(*inc))
	}
	if len(out.Items) > lim {
		out.Items = out.Items[:lim]
		c := out.Items[lim-1].ID
		out.NextCursor = &c
	}
	s.incidentSites(out.Items)
	return out, nil
}

// Incident returns one incident.
func (s *Store) Incident(id int64) (*IncidentRow, error) {
	incs, err := s.queryIncidents(`WHERE id=?`, []any{id})
	if err != nil || len(incs) == 0 {
		return nil, err
	}
	row := IncidentRowOf(*incs[0])
	one := []IncidentRow{row}
	s.incidentSites(one)
	row = one[0]
	row.Acks, err = s.Acks(id)
	return &row, err
}

// Uptime is availability over a window.
type Uptime struct {
	Ratio     *float64 `json:"ratio"` // nil when nothing counted
	DownS     float64  `json:"down_s"`
	UpS       float64  `json:"up_s"`
	ExcludedS float64  `json:"excluded_s"`
	// DegradedS is the part of UpS spent slow (DEGRADED): available, but
	// not performing as configured.
	DegradedS float64 `json:"degraded_s"`
	// Coverage is the share of the window the monitor was watched (up,
	// down or in maintenance): a monitor added an hour ago covers 1/24 of
	// a day.
	Coverage float64 `json:"coverage"`
}

// UptimeOf computes availability = up / (up + down) over [from, to),
// prorating the buckets at the start of the window.
func (s *Store) UptimeOf(monitor string, from, to time.Time) (Uptime, error) {
	var u Uptime
	mid := s.MonitorID(monitor)
	if mid == 0 || !to.After(from) {
		return u, nil
	}
	rows, width, err := s.buckets(mid, from, to)
	if err != nil {
		return u, err
	}
	for _, r := range rows {
		frac := 1.0
		if w := width(r); r.t.Before(from) {
			frac = 1 - float64(from.Sub(r.t))/float64(w)
		}
		u.UpS += r.up * frac
		u.DownS += r.down * frac
		u.ExcludedS += r.ex * frac
		u.DegradedS += r.deg * frac
	}
	if total := u.UpS + u.DownS; total > 0 {
		ratio := u.UpS / total
		u.Ratio = &ratio
	}
	u.Coverage = min(1, (u.UpS+u.DownS+u.ExcludedS)/to.Sub(from).Seconds())
	return u, nil
}

// Cell is one heartbeat bar cell: the worst status in its time slot.
type Cell struct {
	T      time.Time `json:"t"`
	Status string    `json:"s"` // ok | degraded | critical | unknown | "" (no data)
	N      int       `json:"n"`
	Fail   int       `json:"fail"`
}

var severity = map[string]int{"": 0, "pending": 1, "paused": 1, "maintenance": 1, "unknown": 2, "ok": 3, "degraded": 4, "unreachable": 4, "critical": 5}

// Cells splits [from, to) into n slots for the heartbeat bar: from the
// rounds while they are kept, from the aggregates beyond.
func (s *Store) Cells(monitor string, from, to time.Time, n int, sites ...string) ([]Cell, error) {
	if n <= 0 || n > 500 || !to.After(from) {
		return nil, fmt.Errorf("invalid range")
	}
	slot := to.Sub(from) / time.Duration(n)
	cells := make([]Cell, n)
	for i := range cells {
		cells[i].T = from.Add(time.Duration(i) * slot)
	}
	mid := s.MonitorID(monitor)
	if mid == 0 {
		return cells, nil
	}
	idx := func(t time.Time) int { return max(0, min(n-1, int(t.Sub(from)/slot))) }
	mark := func(c *Cell, st string, fail bool) {
		c.N++
		if fail {
			c.Fail++
		}
		if severity[st] > severity[c.Status] {
			c.Status = st
		}
	}
	site := selectedSite(sites)
	if site != "" || !from.Before(s.now().Add(-s.retention().Raw)) {
		rows, err := s.hist.Query(`SELECT at,st FROM rounds WHERE site=? AND mid=? AND at>=? AND at<?`, site, mid, ms(from), ms(to))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var at int64
			var st int
			if err := rows.Scan(&at, &st); err != nil {
				return nil, err
			}
			name := statusNames[st]
			mark(&cells[idx(fromMS(at))], name, name == "critical")
		}
		return cells, rows.Err()
	}
	rows, _, err := s.buckets(mid, from, to)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		st := ""
		switch {
		case r.down > 0:
			st = "critical"
		case r.deg > 0:
			st = "degraded"
		case r.up > 0:
			st = "ok"
		case r.ex > 0:
			st = "unknown"
		}
		mark(&cells[idx(r.t)], st, r.down > 0)
	}
	return cells, nil
}

// LatencyPoint is one point of a latency chart.
type LatencyPoint struct {
	T   time.Time `json:"t"`
	Avg float64   `json:"avg"`
	Max float64   `json:"max"`
}

// Latency returns latency points over [from, to): every round while they
// are kept, 5-minute or hourly averages beyond.
func (s *Store) Latency(monitor string, from, to time.Time, sites ...string) ([]LatencyPoint, error) {
	out := []LatencyPoint{}
	mid := s.MonitorID(monitor)
	if mid == 0 {
		return out, nil
	}
	site := selectedSite(sites)
	if site != "" || !from.Before(s.now().Add(-s.retention().Raw)) {
		rows, err := s.hist.Query(`SELECT at,lat FROM rounds WHERE site=? AND mid=? AND at>=? AND at<? AND lat>0 ORDER BY at`, site, mid, ms(from), ms(to))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var at int64
			var l float64
			if err := rows.Scan(&at, &l); err != nil {
				return nil, err
			}
			out = append(out, LatencyPoint{fromMS(at), l, l})
		}
		return out, rows.Err()
	}
	rows, _, err := s.buckets(mid, from, to)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.latN > 0 {
			out = append(out, LatencyPoint{r.t, r.latSum / float64(r.latN), r.latMax})
		}
	}
	return out, nil
}

// EvidenceRow is one stored probe exchange.
type EvidenceRow struct {
	Site        string       `json:"site,omitempty"`
	SourceEpoch string       `json:"source_epoch,omitempty"`
	OriginalID  int64        `json:"original_id"`
	At          time.Time    `json:"at"`
	Node        string       `json:"node"`
	Result      probe.Result `json:"result"`
}

// EvidenceOf returns recent evidence of a monitor, newest first.
func (s *Store) EvidenceOf(monitor string, limit int, sites ...string) ([]EvidenceRow, error) {
	rows, err := s.core.Query(`SELECT at,node,result,site,source_epoch,CASE WHEN original_id=0 THEN id ELSE original_id END FROM evidence WHERE site=? AND monitor=? ORDER BY id DESC LIMIT ?`, selectedSite(sites), monitor, limitOf(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvidenceRow{}
	for rows.Next() {
		var e EvidenceRow
		var at int64
		var res string
		if err := rows.Scan(&at, &e.Node, &res, &e.Site, &e.SourceEpoch, &e.OriginalID); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		_ = json.Unmarshal([]byte(res), &e.Result)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Snapshot is one applied configuration version.
type Snapshot struct {
	Serial  uint32         `json:"serial"`
	At      time.Time      `json:"at"`
	Source  string         `json:"source"`
	Author  string         `json:"author,omitempty"`
	Message string         `json:"message,omitempty"`
	SHA256  string         `json:"sha256"`
	Summary map[string]int `json:"summary,omitempty"`
	Text    string         `json:"text,omitempty"`
	Term    uint64         `json:"term"` // master term that wrote it
}

// SaveSnapshot records an applied configuration (replacing the same serial).
func (s *Store) SaveSnapshot(sn Snapshot, keep int) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO config_snapshots(serial,at,source,author,message,text,sha256,summary,term) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(serial) DO UPDATE SET at=excluded.at,source=excluded.source,author=excluded.author,message=excluded.message,
		text=excluded.text,sha256=excluded.sha256,summary=excluded.summary,term=excluded.term`,
		sn.Serial, ms(sn.At), sn.Source, sn.Author, sn.Message, sn.Text, sn.SHA256, mustJSON(sn.Summary), sn.Term)
	if err == nil && keep > 0 {
		_, err = s.core.Exec(`DELETE FROM config_snapshots WHERE serial NOT IN (SELECT serial FROM config_snapshots ORDER BY serial DESC LIMIT ?)`, keep)
	}
	return err
}

// Snapshots lists configuration history (without texts), newest first.
func (s *Store) Snapshots(limit int) ([]Snapshot, error) {
	rows, err := s.core.Query(`SELECT serial,at,source,author,message,sha256,summary,term FROM config_snapshots ORDER BY serial DESC LIMIT ?`, limitOf(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		var sn Snapshot
		var at int64
		var src, author, msg, sum sql.NullString
		if err := rows.Scan(&sn.Serial, &at, &src, &author, &msg, &sn.SHA256, &sum, &sn.Term); err != nil {
			return nil, err
		}
		sn.At, sn.Source, sn.Author, sn.Message = fromMS(at), src.String, author.String, msg.String
		_ = json.Unmarshal([]byte(sum.String), &sn.Summary)
		out = append(out, sn)
	}
	return out, rows.Err()
}

// SnapshotText returns one version's text ("" when unknown).
func (s *Store) SnapshotText(serial uint32) (Snapshot, error) {
	var sn Snapshot
	var at int64
	var src, author, msg sql.NullString
	err := s.core.QueryRow(`SELECT serial,at,source,author,message,sha256,text,term FROM config_snapshots WHERE serial=?`, serial).
		Scan(&sn.Serial, &at, &src, &author, &msg, &sn.SHA256, &sn.Text, &sn.Term)
	sn.At, sn.Source, sn.Author, sn.Message = fromMS(at), src.String, author.String, msg.String
	return sn, err
}

// Counters sums daily value counters since a day.
func (s *Store) Counters(since time.Time) (map[string]int, error) {
	rows, err := s.core.Query(`SELECT name, SUM(n) FROM counters WHERE day>=? GROUP BY name`, since.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// Retention is how long history is kept.
type Retention struct {
	Raw, Agg5m, Agg1h, Events, Audit time.Duration
}

// DefaultAuditRetention is how long the audit log is kept unless the server
// is started with another --audit-retention.
const DefaultAuditRetention = 730 * 24 * time.Hour

// SetAuditRetention sets how long the audit log is kept at least: the
// server's --audit-retention. The configuration (retention.audit, which the
// web UI's configuration editor can reach) may only make it longer, so
// nobody can clear the log from the web.
func (s *Store) SetAuditRetention(d time.Duration) { s.auditFloor.Store(int64(d)) }

// AuditRetention is the audit retention in effect: the longer of the
// server's and the configured one.
func (s *Store) AuditRetention(configured time.Duration) time.Duration {
	floor := time.Duration(s.auditFloor.Load())
	if floor <= 0 {
		floor = DefaultAuditRetention
	}
	return max(floor, configured)
}

// RetentionFrom reads the retention tunables.
func RetentionFrom(ts tunables.Set) Retention {
	return Retention{Raw: ts.Duration("retention.raw"), Agg5m: ts.Duration("retention.agg5m"), Agg1h: ts.Duration("retention.agg1h"),
		Events: ts.Duration("retention.events"), Audit: ts.Duration("retention.audit")}
}

// diskFree is FreeRatio, replaceable in tests.
var diskFree = (*Store).FreeRatio

// LowDisk shortens the raw retention to storage.low-disk-raw while the
// data directory's free space is under storage.low-disk-ratio, so a full
// disk costs detail rather than stopping writes. It reports whether it did.
func (s *Store) LowDisk(r *Retention, ts tunables.Set) bool {
	free, ok := diskFree(s)
	if !ok || free >= ts.Float("storage.low-disk-ratio") {
		return false
	}
	if floor := ts.Duration("storage.low-disk-raw"); r.Raw > floor {
		r.Raw = floor
		return true
	}
	return false
}

// Prune deletes expired history and closed incidents.
func (s *Store) Prune(r Retention) error {
	now := s.now()
	err1 := s.pruneRounds(ms(now.Add(-r.Raw)))
	_, err2 := s.hist.Exec(`DELETE FROM agg5m WHERE t<?`, now.Add(-r.Agg5m).Unix())
	if err2 == nil {
		_, err2 = s.hist.Exec(`DELETE FROM hourly WHERE t<?`, now.Add(-r.Agg1h).Unix())
	}
	s.wmu.Lock()
	cut := ms(now.Add(-r.Events))
	_, err3 := s.core.Exec(`DELETE FROM events WHERE at<?`, cut)
	_, err4 := s.core.Exec(`DELETE FROM deliveries WHERE at<?`, cut)
	_, err5 := s.core.Exec(`DELETE FROM incidents WHERE closed=1 AND updated_at<?`, cut)
	if _, err := s.core.Exec(`DELETE FROM decisions WHERE at<?`, cut); err != nil && err3 == nil {
		err3 = err
	}
	_, err6 := s.core.Exec(`DELETE FROM intents WHERE state!='pending' AND updated_at<?`, cut)
	if _, err := s.core.Exec(`DELETE FROM evidence WHERE at<?`, ms(now.Add(-r.Raw))); err != nil && err3 == nil {
		err3 = err
	}
	if _, err := s.core.Exec(`DELETE FROM acks WHERE at<? AND incident_id NOT IN(SELECT id FROM incidents)`, now.Add(-r.Events).Unix()); err != nil && err3 == nil {
		err3 = err
	}
	if _, err := s.core.Exec(`DELETE FROM merged WHERE tbl='incidents' AND local_id NOT IN(SELECT id FROM incidents)`); err != nil && err3 == nil {
		err3 = err
	}
	_, err7 := s.core.Exec(`DELETE FROM sessions WHERE expires_at<?`, ms(now))
	audit := s.AuditRetention(r.Audit)
	err8 := s.pruneAudit(ms(now.Add(-audit)))
	if err8 == nil {
		_, err8 = s.core.Exec(`DELETE FROM site_audit AS old WHERE origin_id<COALESCE((SELECT min(origin_id) FROM site_audit AS kept WHERE kept.site=old.site AND kept.at>=?),(SELECT max(origin_id) FROM site_audit AS tip WHERE tip.site=old.site))`, ms(now.Add(-audit)))
	}
	s.wmu.Unlock()
	for _, err := range []error{err1, err2, err3, err4, err5, err6, err7, err8} {
		if err != nil {
			return err
		}
	}
	return nil
}

// IncidentCounts counts the incidents opened per monitor in [from, to).
func (s *Store) IncidentCounts(from, to time.Time) (map[string]int, error) {
	rows, err := s.core.Query(`SELECT monitor, COUNT(*) FROM incidents WHERE kind='normal' AND opened_at>=? AND opened_at<? GROUP BY monitor`, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var m string
		var n int
		if err := rows.Scan(&m, &n); err != nil {
			return nil, err
		}
		out[m] = n
	}
	return out, rows.Err()
}

// pruneRounds deletes expired rounds in batches, so writers are not held
// up by one long delete.
func (s *Store) pruneRounds(before int64) error {
	for {
		res, err := s.hist.Exec(`DELETE FROM rounds WHERE rowid IN (SELECT rowid FROM rounds WHERE at<? LIMIT 20000)`, before)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n < 20000 {
			return nil
		}
	}
}

// SnapshotsAt returns the newest configuration version written under a
// term (or before it).
func (s *Store) SnapshotsAt(term uint64) (Snapshot, error) {
	var sn Snapshot
	err := s.core.QueryRow(`SELECT serial,sha256,term FROM config_snapshots WHERE term<=? ORDER BY serial DESC LIMIT 1`, term).
		Scan(&sn.Serial, &sn.SHA256, &sn.Term)
	return sn, err
}

// LocalIncidentID resolves only source-owned rows; a reset database cannot ACK an old epoch.
func (s *Store) LocalIncidentID(key string) (int64, error) {
	parts := strings.Split(key, ":")
	if len(parts) != 3 {
		return 0, nil
	}
	original, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || original <= 0 {
		return 0, nil
	}
	var id int64
	err = s.core.QueryRow("SELECT id FROM incidents WHERE site='' AND source_node=? AND source_epoch=? AND (source_id=? OR(source_id=0 AND id=?))", parts[0], parts[1], original, original).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}
func selectedSite(sites []string) string {
	if len(sites) > 0 {
		return sites[0]
	}
	return ""
}

func (s *Store) HistorySites() ([]string, error) {
	rows, err := s.core.Query("SELECT site FROM incidents WHERE site!='' UNION SELECT site FROM events WHERE site!='' UNION SELECT site FROM deliveries WHERE site!='' ORDER BY site")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var site string
		if err := rows.Scan(&site); err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}
