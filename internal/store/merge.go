package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Merging after a split: every site of a cluster writes its own records
// while it checks on its own (a replica cut off from its master, or an old
// master that kept running). When the link is back the site sends what it
// wrote to the master, which adds it tagged with the site and the term.
// Nothing is overwritten and nothing is added twice: the master keeps, per
// site and table, the highest origin id it has merged (merge_marks), and
// the site only sends rows above it. Each site's audit log keeps its own
// hash chain (site_audit), so it can still be verified.

// MergeTables are the tables merged, in order (incidents first: the others
// refer to them).
var MergeTables = []string{"incidents", "events", "decisions", "deliveries", "audit", "evidence", "acks", "rounds"}

// MergeBatch is one site's records above the master's marks. Each row maps
// column names to values; "id" is the origin id. Rounds carry the monitor
// name ("monitor") instead of this server's monitor id.
type MergeBatch struct {
	Epoch  string                      `json:"epoch,omitempty"`
	Site   string                      `json:"site"`
	Term   uint64                      `json:"term"`
	Tables map[string][]map[string]any `json:"tables"`
}

// MergeResult counts what a merge added and skipped (already there).
type MergeResult struct {
	Added   map[string]int `json:"added"`
	Skipped map[string]int `json:"skipped"`
}

// mergeLimit bounds one batch per table; the site sends more until none is
// left.
const mergeLimit = 2000

// MergeMarks returns the highest origin id merged from a site, per table.
func (s *Store) MergeMarks(site string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, db := range []*sql.DB{s.core, s.hist} {
		rows, err := db.Query(`SELECT tbl,last_id FROM merge_marks WHERE site=?`, site)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t string
			var id int64
			if err := rows.Scan(&t, &id); err != nil {
				rows.Close()
				return nil, err
			}
			out[t] = id
		}
		rows.Close()
	}
	return out, nil
}

// SetMergeMarks records that a site's rows up to these ids are already here
// (a new master restored them from the previous master's state).
func (s *Store) SetMergeMarks(site string, marks map[string]int64) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	for t, id := range marks {
		db := s.core
		if t == "rounds" {
			db = s.hist
		}
		if _, err := db.Exec(`INSERT INTO merge_marks(site,tbl,last_id) VALUES(?,?,?)
			ON CONFLICT(site,tbl) DO UPDATE SET last_id=max(last_id,excluded.last_id)`, site, t, id); err != nil {
			return err
		}
	}
	return nil
}

// MaxIDs is the highest id per merged table: what a copy of this store
// already contains.
func (s *Store) MaxIDs() map[string]int64 {
	out := map[string]int64{}
	for _, t := range MergeTables {
		db, col := s.core, "id"
		if t == "rounds" {
			db, col = s.hist, "rowid"
		}
		var id sql.NullInt64
		_ = db.QueryRow(`SELECT max(` + col + `) FROM ` + t).Scan(&id)
		out[t] = id.Int64
	}
	return out
}

// ExportSince returns this server's own records (not ones merged from
// elsewhere) above the marks, at most mergeLimit per table; more reports
// whether some table had more.
func (s *Store) ExportSince(marks map[string]int64) (map[string][]map[string]any, bool, error) {
	out := map[string][]map[string]any{}
	more := false
	epoch, err := s.DBEpoch()
	if err != nil {
		return nil, false, err
	}
	names, err := s.monitorNamesAll()
	if err != nil {
		return nil, false, err
	}
	for _, t := range MergeTables {
		db, q := s.core, `SELECT * FROM `+t+` WHERE id>? AND site='' ORDER BY id LIMIT ?`
		switch t {
		case "audit":
			q = `SELECT * FROM audit WHERE id>? ORDER BY id LIMIT ?`
		case "rounds":
			db, q = s.hist, `SELECT rowid AS id, * FROM rounds WHERE rowid>? AND site='' ORDER BY rowid LIMIT ?`
		}
		rows, err := db.Query(q, marks[t], mergeLimit+1)
		if err != nil {
			return nil, false, fmt.Errorf("export %s: %w", t, err)
		}
		list, err := scanMaps(rows)
		if err != nil {
			return nil, false, fmt.Errorf("export %s: %w", t, err)
		}
		for _, r := range list {
			if t != "audit" && t != "incidents" {
				if str(r["source_epoch"]) == "" {
					r["source_epoch"] = epoch
				}
				if num(r["original_id"]) == 0 {
					r["original_id"] = r["id"]
				}
			}
		}
		if len(list) > mergeLimit {
			list, more = list[:mergeLimit], true
		}
		if t == "rounds" {
			for _, r := range list {
				mid, _ := r["mid"].(int64)
				r["monitor"] = names[mid]
				delete(r, "mid")
			}
		}
		if len(list) > 0 {
			out[t] = list
		}
	}
	return out, more, nil
}

// monitorNamesAll maps every monitor id ever used to its name.
func (s *Store) monitorNamesAll() (map[int64]string, error) {
	rows, err := s.core.Query(`SELECT id,name FROM monitors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func scanMaps(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// columns lists a table's columns.
func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// num reads a JSON or SQL number.
func num(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case int:
		return int64(x)
	}
	return 0
}

// Merge adds a site's records. Rows at or below the marks are skipped, so
// a batch sent twice adds nothing the second time.
func (s *Store) Merge(b MergeBatch) (MergeResult, error) {
	res := MergeResult{Added: map[string]int{}, Skipped: map[string]int{}}
	if b.Site == "" {
		return res, errors.New("a merge needs the site it comes from")
	}
	marks, err := s.MergeMarks(MergeScope(b.Site, b.Epoch))
	if err != nil {
		return res, err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	incMap := map[int64]int64{}
	lookupInc := func(origin int64) int64 {
		if origin == 0 {
			return 0
		}
		if id, ok := incMap[origin]; ok {
			return id
		}
		var id int64
		_ = tx.QueryRow(`SELECT local_id FROM merged WHERE site=? AND tbl='incidents' AND origin_id=?`, MergeScope(b.Site, b.Epoch), origin).Scan(&id)
		return id
	}
	for _, t := range []string{"incidents", "events", "decisions", "deliveries", "audit", "evidence", "acks"} {
		rows := b.Tables[t]
		if len(rows) == 0 {
			continue
		}
		have, err := columns(s.core, t)
		if err != nil {
			return res, err
		}
		last := marks[t]
		for _, r := range rows {
			origin := num(r["id"])
			if origin <= marks[t] {
				res.Skipped[t]++
				continue
			}
			last = max(last, origin)
			if t == "audit" {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO site_audit(site,term,origin_id,at,actor,action,target,area,detail,addr,risk,prev,hash)
					VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, MergeScope(b.Site, b.Epoch), b.Term, origin, num(r["at"]), str(r["actor"]), str(r["action"]),
					str(r["target"]), str(r["area"]), str(r["detail"]), str(r["addr"]), str(r["risk"]), str(r["prev"]), str(r["hash"])); err != nil {
					return res, fmt.Errorf("merge audit: %w", err)
				}
				res.Added[t]++
				continue
			}
			row := map[string]any{}
			for k, v := range r {
				if k != "id" && have[k] {
					row[k] = v
				}
			}
			row["site"] = b.Site
			if t != "incidents" && num(row["original_id"]) == 0 {
				row["original_id"] = origin
			}
			if str(row["source_epoch"]) == "" {
				row["source_epoch"] = b.Epoch
			}
			switch t {
			case "incidents":
				if num(row["source_id"]) == 0 {
					row["source_id"] = origin
				}
				if node := str(row["source_node"]); node != "" && node != b.Site {
					return res, errors.New("incident source differs from authenticated site")
				}
				row["source_node"] = b.Site
				if str(row["source_epoch"]) == "" {
					if str(row["source_epoch"]) == "" {
						row["source_epoch"] = b.Epoch
					}
				}
				row["term"] = int64(b.Term)
				row["closed"] = 1 // the other site's view: history here, never reopened
			case "evidence":
				if str(row["source_epoch"]) == "" {
					row["source_epoch"] = b.Epoch
				}
				if num(row["original_id"]) == 0 {
					row["original_id"] = origin
				}
			case "acks":
				if str(row["source_epoch"]) == "" {
					row["source_epoch"] = b.Epoch
				}
				if num(row["original_id"]) == 0 {
					row["original_id"] = origin
				}
				row["incident_id"] = lookupInc(num(r["incident_id"]))
			case "decisions", "deliveries":
				row["incident_id"] = lookupInc(num(r["incident_id"]))
			}
			if t == "incidents" && str(row["source_epoch"]) != "" {
				var prior int64
				if err := tx.QueryRow(`SELECT id FROM incidents WHERE source_node=? AND source_epoch=? AND source_id=? LIMIT 1`, b.Site, str(row["source_epoch"]), num(row["source_id"])).Scan(&prior); err == nil {
					incMap[origin] = prior
					if _, err := tx.Exec(`INSERT OR IGNORE INTO merged(site,tbl,origin_id,local_id) VALUES(?,'incidents',?,?)`, MergeScope(b.Site, b.Epoch), origin, prior); err != nil {
						return res, err
					}
					res.Skipped[t]++
					continue
				} else if err != sql.ErrNoRows {
					return res, err
				}
			}
			if t != "incidents" {
				var exists int
				if err := tx.QueryRow("SELECT 1 FROM "+t+" WHERE site=? AND source_epoch=? AND original_id=? LIMIT 1", b.Site, str(row["source_epoch"]), num(row["original_id"])).Scan(&exists); err == nil {
					res.Skipped[t]++
					continue
				} else if err != sql.ErrNoRows {
					return res, err
				}
			}
			id, err := insertRow(tx, t, row)
			if err != nil {
				return res, fmt.Errorf("merge %s: %w", t, err)
			}
			if t == "incidents" {
				incMap[origin] = id
				if _, err := tx.Exec(`INSERT OR IGNORE INTO merged(site,tbl,origin_id,local_id) VALUES(?,?,?,?)`, MergeScope(b.Site, b.Epoch), t, origin, id); err != nil {
					return res, err
				}
			}
			res.Added[t]++
		}
		if _, err := tx.Exec(`INSERT INTO merge_marks(site,tbl,last_id) VALUES(?,?,?)
			ON CONFLICT(site,tbl) DO UPDATE SET last_id=max(last_id,excluded.last_id)`, MergeScope(b.Site, b.Epoch), t, last); err != nil {
			return res, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM evidence WHERE site=? AND id NOT IN (SELECT id FROM (SELECT id,ROW_NUMBER() OVER(PARTITION BY monitor,node,source_epoch ORDER BY id DESC) AS rn FROM evidence WHERE site=?) WHERE rn<=20)`, b.Site, b.Site); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, s.mergeRounds(b, marks["rounds"], &res)
}

func (s *Store) mergeRounds(b MergeBatch, mark int64, res *MergeResult) error {
	rows := b.Tables["rounds"]
	if len(rows) == 0 {
		return nil
	}
	have, err := columns(s.hist, "rounds")
	if err != nil {
		return err
	}
	tx, err := s.hist.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	last := mark
	for _, r := range rows {
		origin := num(r["id"])
		if origin <= mark {
			res.Skipped["rounds"]++
			continue
		}
		last = max(last, origin)
		mid := s.MonitorID(str(r["monitor"]))
		if mid == 0 {
			res.Skipped["rounds"]++ // a monitor this server does not have
			continue
		}
		row := map[string]any{"mid": mid, "site": b.Site}
		for k, v := range r {
			if k != "id" && k != "monitor" && k != "mid" && k != "site" && have[k] {
				row[k] = v
			}
		}
		if str(row["source_epoch"]) == "" {
			row["source_epoch"] = b.Epoch
		}
		if num(row["original_id"]) == 0 {
			row["original_id"] = origin
		}
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM rounds WHERE site=? AND source_epoch=? AND original_id=? LIMIT 1`, b.Site, str(row["source_epoch"]), num(row["original_id"])).Scan(&exists); err == nil {
			res.Skipped["rounds"]++
			continue
		} else if err != sql.ErrNoRows {
			return err
		}
		if _, err := insertRow(tx, "rounds", row); err != nil {
			return fmt.Errorf("merge rounds: %w", err)
		}
		res.Added["rounds"]++
	}
	if _, err := tx.Exec(`INSERT INTO merge_marks(site,tbl,last_id) VALUES(?,'rounds',?)
		ON CONFLICT(site,tbl) DO UPDATE SET last_id=max(last_id,excluded.last_id)`, MergeScope(b.Site, b.Epoch), last); err != nil {
		return err
	}
	return tx.Commit()
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func insertRow(tx *sql.Tx, table string, row map[string]any) (int64, error) {
	cols := make([]string, 0, len(row))
	vals := make([]any, 0, len(row))
	for k, v := range row {
		cols = append(cols, `"`+k+`"`)
		vals = append(vals, v)
	}
	q := `INSERT INTO ` + table + `(` + strings.Join(cols, ",") + `) VALUES(` + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + `)`
	r, err := tx.Exec(q, vals...)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// SiteAudit returns a merged site's audit entries, newest first.
func (s *Store) SiteAudit(site string, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.core.Query(`SELECT origin_id,at,actor,action,target,area,detail,addr,risk,hash FROM site_audit WHERE site=? ORDER BY origin_id DESC LIMIT ?`, site, limit)
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
		e.At, e.Site = fromMS(at), site
		if detail != "" {
			_ = unmarshalDetail(detail, &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// VerifySiteAudit checks a merged site's hash chain, as that site wrote it.
func (s *Store) VerifySiteAudit(site string) (int, error) {
	rows, err := s.core.Query(`SELECT origin_id,at,actor,action,target,area,detail,addr,risk,prev,hash FROM site_audit WHERE site=? ORDER BY origin_id`, site)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n, prevHash, first := 0, "", true
	for rows.Next() {
		var id, at int64
		var actor, action, target, area, detail, addr, risk, prev, hash string
		if err := rows.Scan(&id, &at, &actor, &action, &target, &area, &detail, &addr, &risk, &prev, &hash); err != nil {
			return n, err
		}
		if !first && prev != prevHash {
			return n, fmt.Errorf("site %s: entry %d does not chain to the one before", site, id)
		}
		if auditHash(prev, at, actor, action, target, area, detail, addr, risk) != hash {
			return n, fmt.Errorf("site %s: entry %d was altered", site, id)
		}
		prevHash, first = hash, false
		n++
	}
	return n, rows.Err()
}

// MergedSites lists the sites records were merged from.
func (s *Store) MergedSites() ([]string, error) {
	rows, err := s.core.Query(`SELECT DISTINCT site FROM merge_marks ORDER BY site`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// DivergedConfig is a configuration a replaced master kept after the copy
// the new master took over with: shown, never applied by itself.
type DivergedConfig struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	Site       string    `json:"site"`
	Term       uint64    `json:"term"`
	Serial     uint32    `json:"version"`
	BaseSerial uint32    `json:"base_version"` // the version the new master took over with
	SHA256     string    `json:"sha256"`
	Text       string    `json:"text,omitempty"`
}

// AddDiverged keeps a diverged configuration (once per site and content).
func (s *Store) AddDiverged(d DivergedConfig) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT OR IGNORE INTO diverged_configs(at,site,term,serial,sha256,text,base_serial) VALUES(?,?,?,?,?,?,?)`,
		ms(d.At), d.Site, d.Term, d.Serial, d.SHA256, d.Text, d.BaseSerial)
	return err
}

// Diverged lists the kept diverged configurations, newest first.
func (s *Store) Diverged(withText bool) ([]DivergedConfig, error) {
	rows, err := s.core.Query(`SELECT id,at,site,term,serial,sha256,text,base_serial FROM diverged_configs ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DivergedConfig{}
	for rows.Next() {
		var d DivergedConfig
		var at int64
		if err := rows.Scan(&d.ID, &at, &d.Site, &d.Term, &d.Serial, &d.SHA256, &d.Text, &d.BaseSerial); err != nil {
			return nil, err
		}
		d.At = fromMS(at)
		if !withText {
			d.Text = ""
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DismissDiverged removes a kept diverged configuration.
func (s *Store) DismissDiverged(id int64) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`DELETE FROM diverged_configs WHERE id=?`, id)
	return err
}

// MergeScope is an internal provenance key, not a display/site name.
func MergeScope(site, epoch string) string {
	if epoch == "" {
		return site
	}
	return site + "@" + epoch
}
