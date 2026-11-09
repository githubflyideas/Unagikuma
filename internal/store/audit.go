package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// AuditEntry is one action by a person, a token, a command or the system
// itself: who, when, from where, what, on which object, and the details
// (for changes "before" and "after").
type AuditEntry struct {
	ID     int64          `json:"id"`
	At     time.Time      `json:"at"`
	Actor  string         `json:"actor"`
	Action string         `json:"action"` // e.g. user.update, config.apply, session.login
	Target string         `json:"target,omitempty"`
	Area   string         `json:"area,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
	Addr   string         `json:"addr,omitempty"`
	Risk   string         `json:"risk,omitempty"` // "high" for actions worth a second look
	Hash   string         `json:"hash,omitempty"`
	Site   string         `json:"site,omitempty"` // merged from another site of the cluster
}

// Client returns the sign-in tag and the browser or program an entry came
// from (detail.client), where it was recorded.
func (e AuditEntry) Client() (session, userAgent string) {
	c, _ := e.Detail["client"].(map[string]any)
	session, _ = c["session"].(string)
	userAgent, _ = c["ua"].(string)
	return session, userAgent
}

func unmarshalDetail(s string, v *map[string]any) error { return json.Unmarshal([]byte(s), v) }

// highRisk lists actions (or their prefixes, ending in ".") that change
// who may do what, remove things, or move the whole installation.
var highRisk = []string{
	"user.", "token.", "secret.", "session.login_failed", "settings.", "registration.",
	"backup.restore", "backup.download", "export.", "import.", "config.rollback", "config.bump",
	"cluster.", "node.", "system.hold_released", "system.abnormal_exit", "system.clock_changed",
	"audit.", "maintenance.", "silence", "monitor.delete", "channel.delete", "policy.delete",
	"people.delete", "contact.delete", "oncall.delete",
}

// RiskOf says whether an action is high-risk.
func RiskOf(action string, detail map[string]any) string {
	for _, p := range highRisk {
		if action == strings.TrimSuffix(p, ".") || strings.HasPrefix(action, p) {
			return "high"
		}
	}
	if action == "config.apply" && detail != nil && detail["removed"] != nil {
		return "high" // records were deleted
	}
	return ""
}

// auditHash chains an entry to the one before it.
func auditHash(prev string, at int64, actor, action, target, area, detail, addr, risk string) string {
	h := sha256.New()
	for _, f := range []string{prev, strconv.FormatInt(at, 10), actor, action, target, area, detail, addr, risk} {
		h.Write([]byte(strconv.Itoa(len(f))))
		h.Write([]byte{':'})
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Audit appends an entry to the log.
func (s *Store) Audit(e AuditEntry) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	detail := ""
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(b)
	}
	if e.Risk == "" {
		e.Risk = RiskOf(e.Action, e.Detail)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev, err := auditHead(tx)
	if err != nil {
		return err
	}
	at := ms(e.At)
	hash := auditHash(prev, at, e.Actor, e.Action, e.Target, e.Area, detail, e.Addr, e.Risk)
	if _, err := tx.Exec(`INSERT INTO audit(at,actor,action,target,area,detail,addr,risk,prev,hash) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		at, e.Actor, e.Action, e.Target, e.Area, detail, e.Addr, e.Risk, prev, hash); err != nil {
		return err
	}
	return tx.Commit()
}

type queryer interface {
	QueryRow(string, ...any) *sql.Row
}

// auditHead is the hash the next entry chains to: the newest entry's, or
// the anchor left by pruning when the log is empty.
func auditHead(q queryer) (string, error) {
	var h string
	err := q.QueryRow(`SELECT hash FROM audit ORDER BY id DESC LIMIT 1`).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		err = q.QueryRow(`SELECT value FROM settings WHERE key='audit_anchor'`).Scan(&h)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
	}
	return h, err
}

// chainAudit gives entries written before the log was chained their
// hashes, oldest first. Run at open.
func (s *Store) chainAudit() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var n int
	if err := s.core.QueryRow(`SELECT COUNT(*) FROM audit WHERE hash=''`).Scan(&n); err != nil || n == 0 {
		return err
	}
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id,at,actor,action,target,area,detail,addr,hash FROM audit ORDER BY id`)
	if err != nil {
		return err
	}
	type upd struct {
		id         int64
		risk, prev string
		hash       string
	}
	var todo []upd
	prev := ""
	for rows.Next() {
		var id, at int64
		var actor, action, target, area, detail, addr, hash string
		if err := rows.Scan(&id, &at, &actor, &action, &target, &area, &detail, &addr, &hash); err != nil {
			rows.Close()
			return err
		}
		if hash != "" {
			prev = hash
			continue
		}
		var d map[string]any
		_ = json.Unmarshal([]byte(detail), &d)
		risk := RiskOf(action, d)
		hash = auditHash(prev, at, actor, action, target, area, detail, addr, risk)
		todo = append(todo, upd{id, risk, prev, hash})
		prev = hash
	}
	rows.Close()
	for _, u := range todo {
		if _, err := tx.Exec(`UPDATE audit SET risk=?, prev=?, hash=? WHERE id=?`, u.risk, u.prev, u.hash, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// pruneAudit removes entries older than cut, leaving the hash of the last
// removed one as the anchor the chain now starts from. Caller holds wmu.
func (s *Store) pruneAudit(cut int64) error {
	var last string
	err := s.core.QueryRow(`SELECT hash FROM audit WHERE at<? ORDER BY id DESC LIMIT 1`, cut).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES('audit_anchor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, last); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM audit WHERE id <= (SELECT id FROM audit WHERE hash=?)`, last); err != nil {
		return err
	}
	return tx.Commit()
}

// AuditCheck is the result of verifying the audit log.
type AuditCheck struct {
	OK      bool      `json:"ok"`
	Entries int       `json:"entries"`
	First   time.Time `json:"first,omitzero"`
	Last    time.Time `json:"last,omitzero"`
	Head    string    `json:"head"`             // hash of the newest entry: note it to detect removal at the end later
	BadID   int64     `json:"bad_id,omitempty"` // first entry that does not match
	Problem string    `json:"problem,omitempty"`
}

// VerifyAudit recomputes the hash chain. An entry that was changed, or an
// entry inserted or removed in the middle, breaks it at that point. A
// removal of the newest entries is found by comparing Head with a value
// noted earlier (see unagikuma audit verify --head).
func (s *Store) VerifyAudit() (AuditCheck, error) {
	var c AuditCheck
	var anchor string
	_ = s.core.QueryRow(`SELECT value FROM settings WHERE key='audit_anchor'`).Scan(&anchor)
	rows, err := s.core.Query(`SELECT id,at,actor,action,target,area,detail,addr,risk,prev,hash FROM audit ORDER BY id`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	prev := anchor
	for rows.Next() {
		var id, at int64
		var actor, action, target, area, detail, addr, risk, p, hash string
		if err := rows.Scan(&id, &at, &actor, &action, &target, &area, &detail, &addr, &risk, &p, &hash); err != nil {
			return c, err
		}
		c.Entries++
		if c.Entries == 1 {
			c.First = fromMS(at)
		}
		c.Last = fromMS(at)
		if c.BadID == 0 {
			switch {
			case p != prev:
				c.BadID, c.Problem = id, fmt.Sprintf("entry %d does not follow the one before it: an entry was removed or inserted here", id)
			case auditHash(prev, at, actor, action, target, area, detail, addr, risk) != hash:
				c.BadID, c.Problem = id, fmt.Sprintf("entry %d was changed after it was written", id)
			}
		}
		prev = hash
	}
	c.Head = prev
	c.OK = c.BadID == 0
	return c, rows.Err()
}

// AuditContains says whether a hash noted earlier is still in the log (or
// is the pruning anchor): if not, entries were removed.
func (s *Store) AuditContains(hash string) bool {
	var n int
	_ = s.core.QueryRow(`SELECT COUNT(*) FROM audit WHERE hash=?`, hash).Scan(&n)
	if n > 0 {
		return true
	}
	var anchor string
	_ = s.core.QueryRow(`SELECT value FROM settings WHERE key='audit_anchor'`).Scan(&anchor)
	return anchor == hash
}

// AuditQuery filters the audit log; empty fields match everything.
type AuditQuery struct {
	Actor, Action, Target, Area, Addr string
	Session                           string // a sign-in's tag (detail.client.session)
	Risk                              string // "high": only high-risk actions
	From, To                          time.Time
	Before                            int64 // page: entries with a smaller id
	Limit                             int   // at most 1000 (10000 for exports)
	Export                            bool
}

// AuditLog returns matching entries, newest first.
func (s *Store) AuditLog(q AuditQuery) ([]AuditEntry, error) {
	var where []string
	var args []any
	if q.Actor != "" {
		where, args = append(where, "actor=?"), append(args, q.Actor)
	}
	if q.Action != "" {
		// "user" matches user.create, user.update...
		where, args = append(where, "(action=? OR action LIKE ?)"), append(args, q.Action, q.Action+".%")
	}
	if q.Target != "" {
		where, args = append(where, "target=?"), append(args, q.Target)
	}
	if q.Area != "" {
		where, args = append(where, "area=?"), append(args, q.Area)
	}
	if q.Addr != "" {
		where, args = append(where, "addr=?"), append(args, q.Addr)
	}
	if q.Session != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q.Session)
		where, args = append(where, `detail LIKE ? ESCAPE '\'`), append(args, `%"session":"`+esc+`"%`)
	}
	if q.Risk != "" {
		where, args = append(where, "risk=?"), append(args, q.Risk)
	}
	if !q.From.IsZero() {
		where, args = append(where, "at>=?"), append(args, ms(q.From))
	}
	if !q.To.IsZero() {
		where, args = append(where, "at<?"), append(args, ms(q.To))
	}
	if q.Before > 0 {
		where, args = append(where, "id<?"), append(args, q.Before)
	}
	max := 1000
	if q.Export {
		max = 10000
	}
	if q.Limit <= 0 || q.Limit > max {
		q.Limit = 200
		if q.Export {
			q.Limit = max
		}
	}
	sql := `SELECT id,at,actor,action,target,area,detail,addr,risk,hash FROM audit`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY id DESC LIMIT ?"
	args = append(args, q.Limit)
	rows, err := s.core.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var at int64
		var detail string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Target, &e.Area, &detail, &e.Addr, &e.Risk, &e.Hash); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// WriteAudit writes entries as CSV (one row each, detail as JSON) or JSON.
func WriteAudit(w io.Writer, format string, items []AuditEntry) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "time", "actor", "address", "action", "object", "area", "risk", "detail", "hash", "session", "user_agent"})
		for _, e := range items {
			d := ""
			if len(e.Detail) > 0 {
				b, _ := json.Marshal(e.Detail)
				d = string(b)
			}
			session, ua := e.Client()
			_ = cw.Write([]string{strconv.FormatInt(e.ID, 10), e.At.UTC().Format(time.RFC3339), e.Actor, e.Addr, e.Action, e.Target, e.Area, e.Risk, d, e.Hash, session, ua})
		}
		cw.Flush()
		return cw.Error()
	}
	return errors.New("the format must be csv or json")
}
