package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// The engine.Sink implementation. Incidents and intents are written
// synchronously (notification crash safety depends on it); verdicts,
// evidence, events and counters are batched.

var _ engine.Sink = (*Store)(nil)

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// SaveState persists a monitor's runtime state.
func (s *Store) SaveState(r engine.StateRecord) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO monitor_state(name,state,status,reason,detail,latency_ms,last_check,last_change,incident_id,def_hash)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET state=excluded.state,status=excluded.status,reason=excluded.reason,
		detail=excluded.detail,latency_ms=excluded.latency_ms,last_check=excluded.last_check,last_change=excluded.last_change,
		incident_id=excluded.incident_id,def_hash=excluded.def_hash`,
		r.Name, mustJSON(r.State), r.Status, r.Reason, r.Detail, r.LatencyMS, ms(r.LastCheck), ms(r.LastChange), r.IncidentID, r.DefHash)
	s.logErr("save state", err)
}

// DeleteState forgets a monitor.
func (s *Store) DeleteState(name string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`DELETE FROM monitor_state WHERE name=?`, name)
	s.logErr("delete state", err)
}

// States loads every persisted monitor state (for engine.Restore).
func (s *Store) States() ([]engine.StateRecord, error) {
	rows, err := s.core.Query(`SELECT name,state,status,reason,detail,latency_ms,last_check,last_change,incident_id,def_hash FROM monitor_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []engine.StateRecord
	for rows.Next() {
		var r engine.StateRecord
		var st string
		var reason, detail, hash sql.NullString
		var lat sql.NullFloat64
		var lc, lch, inc sql.NullInt64
		if err := rows.Scan(&r.Name, &st, &r.Status, &reason, &detail, &lat, &lc, &lch, &inc, &hash); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(st), &r.State)
		r.Reason, r.Detail, r.DefHash, r.LatencyMS = reason.String, detail.String, hash.String, lat.Float64
		r.LastCheck, r.LastChange, r.IncidentID = fromMS(lc.Int64), fromMS(lch.Int64), inc.Int64
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveIncident inserts (assigning inc.ID) or updates an incident.
func (s *Store) SaveIncident(inc *engine.Incident) error {
	if inc.SourceEpoch == "" {
		epoch, err := s.DBEpoch()
		if err != nil {
			return err
		}
		inc.SourceEpoch = epoch
	}
	if inc.SourceNode == "" {
		inc.SourceNode, _ = s.Setting("source_node")
		if inc.SourceNode == "" {
			inc.SourceNode = "local"
		}
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	st := inc.State
	args := []any{inc.Monitor, inc.Kind, inc.System, inc.Policy, inc.Reason, inc.Detail, inc.Title, inc.AckComment, inc.Summary,
		mustJSON(st), ms(st.OpenedAt), ms(st.ResolvedAt), st.Closed, st.Acked, st.Drill, ms(s.now()), inc.SourceNode, inc.SourceEpoch, inc.OriginalID}
	if inc.ID == 0 {
		res, err := s.core.Exec(`INSERT INTO incidents(monitor,kind,system,policy,reason,detail,title,ack_comment,summary,state,opened_at,resolved_at,closed,acked,drill,updated_at,source_node,source_epoch,source_id)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
		if err != nil {
			return err
		}
		inc.ID, err = res.LastInsertId()
		inc.OriginalID = inc.ID
		inc.GlobalID = engine.IncidentKey(inc.SourceNode, inc.SourceEpoch, inc.OriginalID)
		return err
	}
	_, err := s.core.Exec(`UPDATE incidents SET monitor=?,kind=?,system=?,policy=?,reason=?,detail=?,title=?,ack_comment=?,summary=?,state=?,
		opened_at=?,resolved_at=?,closed=?,acked=?,drill=?,updated_at=?,source_node=?,source_epoch=?,source_id=? WHERE id=?`, append(args, inc.ID)...)
	inc.GlobalID = engine.IncidentKey(inc.SourceNode, inc.SourceEpoch, inc.OriginalID)
	return err
}

// SaveIntent creates or updates a notification intent.
func (s *Store) SaveIntent(in *engine.Intent) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	covers, chain := "", ""
	if len(in.Covers) > 0 {
		covers = mustJSON(in.Covers)
	}
	if len(in.Chain) > 0 {
		chain = mustJSON(in.Chain)
	}
	_, err := s.core.Exec(`INSERT INTO intents(id,incident_id,seq,kind,policy,message,delivery,state,created_at,due_at,updated_at,system,force_fail,attempts,covers,chain)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET delivery=excluded.delivery,state=excluded.state,
		updated_at=excluded.updated_at,message=excluded.message,attempts=excluded.attempts,covers=excluded.covers`,
		in.ID, in.IncidentID, in.Seq, in.Kind, in.Policy, mustJSON(in.Message), mustJSON(in.Delivery), in.State,
		ms(in.CreatedAt), ms(in.DueAt), ms(in.UpdatedAt), in.System, in.ForceFail, mustJSON(in.Attempts), covers, chain)
	return err
}

// OpenIncidents loads incidents that are not closed (for engine.Restore).
func (s *Store) OpenIncidents() ([]*engine.Incident, error) {
	return s.queryIncidents(`WHERE closed=0 AND site=''`, nil)
}

func (s *Store) queryIncidents(where string, args []any) ([]*engine.Incident, error) {
	rows, err := s.core.Query(`SELECT id,monitor,kind,system,policy,reason,detail,title,ack_comment,summary,state,source_node,source_epoch,source_id FROM incidents `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*engine.Incident
	for rows.Next() {
		inc := &engine.Incident{}
		var system, policy, reason, detail, title, ack, summary sql.NullString
		var st string
		if err := rows.Scan(&inc.ID, &inc.Monitor, &inc.Kind, &system, &policy, &reason, &detail, &title, &ack, &summary, &st, &inc.SourceNode, &inc.SourceEpoch, &inc.OriginalID); err != nil {
			return nil, err
		}
		inc.System, inc.Policy, inc.Reason, inc.Detail, inc.Title = system.String, policy.String, reason.String, detail.String, title.String
		inc.AckComment, inc.Summary = ack.String, summary.String
		_ = json.Unmarshal([]byte(st), &inc.State)
		if inc.OriginalID == 0 {
			inc.OriginalID = inc.ID
		}
		inc.GlobalID = engine.IncidentKey(inc.SourceNode, inc.SourceEpoch, inc.OriginalID)
		out = append(out, inc)
	}
	return out, rows.Err()
}

// PendingIntents loads undelivered intents, oldest first.
func (s *Store) PendingIntents() ([]*engine.Intent, error) {
	rows, err := s.core.Query(`SELECT id,incident_id,seq,kind,policy,message,delivery,state,created_at,due_at,updated_at,system,force_fail,attempts,covers,chain
		FROM intents WHERE state='pending' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*engine.Intent
	for rows.Next() {
		in := &engine.Intent{}
		var msg, del string
		var attempts, policy sql.NullString
		var created, due, updated int64
		var covers, chain string
		if err := rows.Scan(&in.ID, &in.IncidentID, &in.Seq, &in.Kind, &policy, &msg, &del, &in.State, &created, &due, &updated, &in.System, &in.ForceFail, &attempts, &covers, &chain); err != nil {
			return nil, err
		}
		if covers != "" {
			_ = json.Unmarshal([]byte(covers), &in.Covers)
		}
		if chain != "" {
			_ = json.Unmarshal([]byte(chain), &in.Chain)
		}
		in.Policy = policy.String
		_ = json.Unmarshal([]byte(msg), &in.Message)
		_ = json.Unmarshal([]byte(del), &in.Delivery)
		_ = json.Unmarshal([]byte(attempts.String), &in.Attempts)
		in.CreatedAt, in.DueAt, in.UpdatedAt = fromMS(created), fromMS(due), fromMS(updated)
		out = append(out, in)
	}
	return out, rows.Err()
}

// Delivery logs a finished delivery.
func (s *Store) Delivery(d engine.DeliveryRecord) { s.batch.add(item{delivery: &d}) }

// Verdict records one judged round (batched).
func (s *Store) Verdict(v engine.VerdictRecord) { s.batch.add(item{verdict: &v}) }

// Evidence keeps the raw exchange of a round (batched, ring per monitor).
func (s *Store) Evidence(name, node string, at time.Time, r probe.Result) {
	s.batch.add(item{evidence: &evidenceItem{name, node, at, r}})
}

// Event records an event (batched).
func (s *Store) Event(ev engine.Event) {
	s.batch.add(item{event: &ev})
	s.auditSystem(ev)
}

// systemAudited are the system events the audit log keeps: what the
// monitor itself decided or suffered (holding alerts during a local
// outage, releasing them, restarts, crashes, clock jumps).
var systemAudited = map[string]bool{
	"system_opened": true, "system_resolved": true, "hold_released": true, "outage_summary": true,
	"outage_backdated": true, "restarted": true, "abnormal_exit": true, "clock_changed": true,
}

func (s *Store) auditSystem(ev engine.Event) {
	if ev.Type != engine.EventSystem {
		return
	}
	code, _ := ev.Data["msg"].(string)
	if c, ok := ev.Data["audit"].(string); ok {
		code = c // audited, but shown by its own message
	}
	if !systemAudited[code] {
		return
	}
	d := map[string]any{"message": ev.Message}
	target := ""
	for k, v := range ev.Data {
		switch k {
		case "msg", "audit":
		case "system":
			target, _ = v.(string)
		default:
			d[k] = v
		}
	}
	if err := s.Audit(AuditEntry{At: ev.At, Actor: "system", Action: "system." + code, Target: target, Detail: d}); err != nil && s.log != nil {
		s.log.Error("audit", "err", err)
	}
}

// Counter adds to a daily value counter.
// Decision records a step in the decision ledger.
func (s *Store) Decision(d engine.Decision) { s.batch.add(item{decision: &d}) }

func (s *Store) Counter(name string, n int) { s.batch.add(item{counter: name, n: n}) }

// Flush writes pending batched items now.
func (s *Store) Flush() { s.batch.flushNow() }

func (s *Store) logErr(what string, err error) {
	if err != nil && !errors.Is(err, sql.ErrConnDone) {
		s.log.Error("store: "+what, "err", err)
	}
}

type evidenceItem struct {
	name, node string
	at         time.Time
	r          probe.Result
}

type item struct {
	verdict  *engine.VerdictRecord
	delivery *engine.DeliveryRecord
	evidence *evidenceItem
	event    *engine.Event
	decision *engine.Decision
	counter  string
	n        int
}

// batcher accumulates history writes and commits them periodically.
type batcher struct {
	s        *Store
	mu       sync.Mutex
	items    []item
	every    time.Duration
	keep     int
	quit     chan struct{}
	done     chan struct{}
	last     map[int64]lastVerdict // per monitor id, for uptime aggregation
	stopOnce sync.Once
}

type lastVerdict struct {
	at       time.Time
	class    string // up | degraded | soft | down | excluded
	interval time.Duration
	latency  float64
	soft     []softSpan // SOFT time not yet attributed
}

type softSpan struct {
	from time.Time
	d    time.Duration
}

func newBatcher(s *Store, every time.Duration, keep int) *batcher {
	if keep <= 0 {
		keep = 20
	}
	b := &batcher{s: s, every: every, keep: keep, quit: make(chan struct{}), done: make(chan struct{}), last: map[int64]lastVerdict{}}
	go b.loop()
	return b
}

func (b *batcher) add(it item) {
	b.mu.Lock()
	b.items = append(b.items, it)
	n := len(b.items)
	b.mu.Unlock()
	if n >= 5000 {
		b.flushNow()
	}
}

func (b *batcher) loop() {
	defer close(b.done)
	t := time.NewTicker(b.every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.flushNow()
		case <-b.quit:
			b.flushNow()
			return
		}
	}
}

func (b *batcher) stop() {
	b.stopOnce.Do(func() {
		close(b.quit)
		<-b.done
	})
}

var flushMu sync.Mutex

func (b *batcher) flushNow() {
	flushMu.Lock()
	defer flushMu.Unlock()
	b.mu.Lock()
	items := b.items
	b.items = nil
	b.mu.Unlock()
	if len(items) == 0 {
		return
	}
	start := time.Now()
	var retry []item
	if !b.flushHistory(items) {
		for _, it := range items {
			if it.verdict != nil {
				retry = append(retry, it)
			}
		}
	}
	if !b.flushCore(items) {
		for _, it := range items {
			if it.verdict == nil {
				retry = append(retry, it)
			}
		}
	}
	if len(retry) > 0 {
		b.requeue(retry)
	} else {
		b.s.writes.ok()
	}
	b.s.flushes.Add(1)
	b.s.flushMicros.Add(time.Since(start).Microseconds())
	b.s.flushedItems.Add(int64(len(items)))
}

// WriteStats is how the batched history writes are keeping up.
type WriteStats struct {
	Flushes      int64
	FlushSeconds float64 // total time spent writing
	Items        int64   // results, events and counters written
	Queued       int     // waiting for the next flush
}

// WriteStats returns the batch writer's counters since start.
func (s *Store) WriteStats() WriteStats {
	s.batch.mu.Lock()
	q := len(s.batch.items)
	s.batch.mu.Unlock()
	return WriteStats{Flushes: s.flushes.Load(), FlushSeconds: float64(s.flushMicros.Load()) / 1e6, Items: s.flushedItems.Load(), Queued: q}
}

// beginBatch starts a batch transaction, replaceable in tests (a full disk
// is hard to come by).
var beginBatch = func(db *sql.DB) (*sql.Tx, error) { return db.Begin() }

// maxRetry bounds what a failing disk keeps waiting in memory; beyond it
// the oldest writes are lost (and counted).
const maxRetry = 50000

// requeue puts writes that failed back in front of the queue, keeping at
// most maxRetry of them.
func (b *batcher) requeue(items []item) {
	b.mu.Lock()
	all := append(items, b.items...)
	lost := 0
	if len(all) > maxRetry {
		lost = len(all) - maxRetry
		all = all[lost:]
	}
	b.items = all
	b.mu.Unlock()
	b.s.writes.failed(lost)
}

// writeHealth tracks whether the batched writes reach the disk.
type writeHealth struct {
	mu      sync.Mutex
	since   time.Time
	err     string
	dropped int64
}

func (w *writeHealth) failed(lost int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.since.IsZero() {
		w.since = time.Now()
	}
	w.dropped += int64(lost)
}

func (w *writeHealth) fail(err error) {
	if err == nil || errors.Is(err, sql.ErrConnDone) {
		return
	}
	w.mu.Lock()
	w.err = err.Error()
	w.mu.Unlock()
}

func (w *writeHealth) ok() {
	w.mu.Lock()
	w.since, w.err = time.Time{}, ""
	w.mu.Unlock()
}

// WriteHealth is whether results and events reach the disk: since when
// writing fails, why, and how many writes were lost (the rest are kept and
// retried).
type WriteHealth struct {
	Failing bool      `json:"failing"`
	Since   time.Time `json:"since,omitzero"`
	Error   string    `json:"error,omitempty"`
	Lost    int64     `json:"lost"`
	Waiting int       `json:"waiting"`
}

// WriteHealth reports the batch writer's state.
func (s *Store) WriteHealth() WriteHealth {
	s.batch.mu.Lock()
	q := len(s.batch.items)
	s.batch.mu.Unlock()
	w := &s.writes
	w.mu.Lock()
	defer w.mu.Unlock()
	return WriteHealth{Failing: !w.since.IsZero(), Since: w.since, Error: w.err, Lost: w.dropped, Waiting: q}
}

// flushHistory writes results; false when the batch did not reach the disk.
func (b *batcher) flushHistory(items []item) bool {
	s := b.s
	type resolved struct {
		v   *engine.VerdictRecord
		mid int64
	}
	var vs []resolved
	for _, it := range items {
		if it.verdict == nil {
			continue
		}
		mid, err := s.idFor(it.verdict.Monitor)
		if err != nil {
			s.logErr("monitor id", err)
			continue
		}
		vs = append(vs, resolved{it.verdict, mid})
	}
	if len(vs) == 0 {
		return true
	}
	tx, err := beginBatch(s.hist)
	if err != nil {
		s.logErr("history batch", err)
		s.writes.fail(err)
		return false
	}
	defer tx.Rollback()
	ins, _ := tx.Prepare(`INSERT INTO rounds(mid,at,st,flags,lat,reason,nodes) VALUES(?,?,?,?,?,?,?)`)
	w := &aggWriter{}
	for _, table := range []string{"agg5m", "hourly"} {
		st, _ := tx.Prepare(`INSERT INTO ` + table + `(mid,t,up_s,down_s,ex_s,deg_s,lat_sum,lat_n,lat_max) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(mid,t) DO UPDATE SET up_s=up_s+excluded.up_s, down_s=down_s+excluded.down_s, ex_s=ex_s+excluded.ex_s,
			deg_s=deg_s+excluded.deg_s, lat_sum=lat_sum+excluded.lat_sum, lat_n=lat_n+excluded.lat_n, lat_max=max(lat_max, excluded.lat_max)`)
		w.stmts = append(w.stmts, st)
	}
	w.log = s.logErr
	for _, r := range vs {
		v := r.v
		var fails, reason, lat any
		if len(v.FailNodes) > 0 {
			fails = mustJSON(v.FailNodes)
		}
		if v.Reason != "" {
			reason = v.Reason
		}
		if v.LatencyMS > 0 {
			lat = v.LatencyMS
		}
		code, ok := statusCodes[v.Status]
		if !ok {
			code = statusCodes["unknown"]
		}
		flags := verdictCodes[string(v.Verdict)] << verdictShift
		if v.Hard {
			flags |= flagHard
		}
		if v.Excluded {
			flags |= flagExcluded
		}
		if _, err := ins.Exec(r.mid, ms(v.At), code, flags, lat, reason, fails); err != nil {
			s.logErr("insert round", err)
			continue
		}
		b.aggregate(w, r.mid, v)
	}
	if err := tx.Commit(); err != nil {
		s.logErr("history commit", err)
		s.writes.fail(err)
		return false
	}
	return true
}

// aggWriter adds seconds and latency to the 5-minute and hourly buckets.
type aggWriter struct {
	stmts []*sql.Stmt // agg5m, hourly
	log   func(string, error)
}

var bucketWidths = []time.Duration{5 * time.Minute, time.Hour}

func (w *aggWriter) add(mid int64, at time.Time, up, down, ex, deg, lat float64) {
	n := 0
	if lat > 0 {
		n = 1
	}
	for i, st := range w.stmts {
		_, err := st.Exec(mid, at.Truncate(bucketWidths[i]).Unix(), up, down, ex, deg, lat, n, lat)
		w.log("aggregate", err)
	}
}

// classOf maps a recorded round to the uptime class. A SOFT failure is
// "soft": it counts as down when the failure turns HARD and as up when it
// recovers first. Unknown, pending, paused and maintenance are excluded
// from the denominator.
func classOf(v *engine.VerdictRecord) string {
	switch {
	case v.Excluded:
		return "excluded"
	case v.Status == engine.StatusCritical && v.Hard:
		return "down"
	case v.Status == engine.StatusCritical:
		return "soft"
	case v.Status == engine.StatusDegraded:
		return "degraded"
	case v.Status == engine.StatusOK:
		return "up"
	}
	return "excluded"
}

// aggregate attributes the time since the previous round of the monitor
// to the previous round's class, split across buckets; a pause longer
// than gap-factor intervals is a gap (excluded). Time spent SOFT is held
// until the outcome is known.
func (b *batcher) aggregate(w *aggWriter, mid int64, v *engine.VerdictRecord) {
	prev, ok := b.last[mid]
	cur := lastVerdict{at: v.At, class: classOf(v), interval: v.Interval, latency: v.LatencyMS, soft: prev.soft}
	if v.LatencyMS > 0 && cur.class != "excluded" {
		w.add(mid, v.At, 0, 0, 0, 0, v.LatencyMS)
	}
	if ok && v.At.After(prev.at) {
		span := v.At.Sub(prev.at)
		limit := time.Duration(float64(prev.interval) * b.s.gapFactor())
		attributed, gap := span, time.Duration(0)
		if prev.interval > 0 && span > limit {
			attributed, gap = limit, span-limit
		}
		if prev.class == "soft" {
			cur.soft = append(cur.soft, softSpan{prev.at, attributed})
		} else {
			b.spread(w, mid, prev.at, attributed, prev.class)
		}
		if gap > 0 {
			b.spread(w, mid, prev.at.Add(attributed), gap, "excluded")
		}
	}
	// Settle held SOFT time once the outcome is known (or the hold grows
	// past its bound: a monitor stuck SOFT counts as up, as it was never
	// declared down).
	if len(cur.soft) > 0 && (cur.class != "soft" || len(cur.soft) > 1000) {
		as := "up"
		if cur.class == "down" {
			as = "down"
		}
		for _, sp := range cur.soft {
			b.spread(w, mid, sp.from, sp.d, as)
		}
		cur.soft = nil
	}
	b.last[mid] = cur
}

func (b *batcher) spread(w *aggWriter, mid int64, from time.Time, d time.Duration, class string) {
	for d > 0 {
		end := from.Truncate(5 * time.Minute).Add(5 * time.Minute)
		chunk := min(d, end.Sub(from))
		secs := chunk.Seconds()
		var up, down, ex, deg float64
		switch class {
		case "up":
			up = secs
		case "degraded":
			up, deg = secs, secs
		case "down":
			down = secs
		default:
			ex = secs
		}
		w.add(mid, from, up, down, ex, deg, 0)
		from, d = from.Add(chunk), d-chunk
	}
}

// flushCore writes events, deliveries, evidence and counters; false when
// the batch did not reach the disk.
func (b *batcher) flushCore(items []item) bool {
	s := b.s
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := beginBatch(s.core)
	if err != nil {
		s.logErr("core batch", err)
		s.writes.fail(err)
		return false
	}
	defer tx.Rollback()
	touched := map[[2]string]bool{}
	for _, it := range items {
		switch {
		case it.event != nil:
			ev := it.event
			var data any
			if len(ev.Data) > 0 {
				data = mustJSON(ev.Data)
			}
			_, err = tx.Exec(`INSERT INTO events(at,type,severity,monitor,message,data) VALUES(?,?,?,?,?,?)`, ms(ev.At), ev.Type, ev.Severity, ev.Monitor, ev.Message, data)
		case it.delivery != nil:
			d := it.delivery
			_, err = tx.Exec(`INSERT INTO deliveries(intent_id,incident_id,kind,seq,at,attempts,ok,fallback,drill,delayed_s) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				d.IntentID, d.IncidentID, d.Kind, d.Seq, ms(d.At), mustJSON(d.Attempts), d.OK, d.Fallback, d.Drill, int(d.Delayed.Seconds()))
		case it.decision != nil:
			d := it.decision
			_, err = tx.Exec(`INSERT INTO decisions(at,incident_id,intent_id,monitor,step,data) VALUES(?,?,?,?,?,?)`,
				ms(d.At), d.IncidentID, d.IntentID, d.Monitor, d.Step, mustJSON(d))
		case it.evidence != nil:
			e := it.evidence
			_, err = tx.Exec(`INSERT INTO evidence(monitor,node,at,result) VALUES(?,?,?,?)`, e.name, e.node, ms(e.at), mustJSON(e.r))
			touched[[2]string{e.name, e.node}] = true
		case it.counter != "":
			_, err = tx.Exec(`INSERT INTO counters(day,name,n) VALUES(?,?,?) ON CONFLICT(day,name) DO UPDATE SET n=n+excluded.n`,
				s.now().Format("2006-01-02"), it.counter, it.n)
		default:
			continue
		}
		s.logErr("batch write", err)
	}
	for k := range touched {
		_, err := tx.Exec(`DELETE FROM evidence WHERE site='' AND monitor=? AND node=? AND id NOT IN
			(SELECT id FROM evidence WHERE site='' AND monitor=? AND node=? ORDER BY id DESC LIMIT ?)`, k[0], k[1], k[0], k[1], b.keep)
		s.logErr("evidence ring", err)
	}
	if err := tx.Commit(); err != nil {
		s.logErr("core commit", err)
		s.writes.fail(err)
		return false
	}
	return true
}
