package store

import (
	"fmt"
	"strconv"
	"time"
)

// Rounds store the status as a small code and the verdict and flags as
// bits, which keeps a row of the ring at a few dozen bytes.

var statusCodes = map[string]int{"ok": 1, "degraded": 2, "critical": 3, "unknown": 4, "pending": 5, "paused": 6, "maintenance": 7, "unreachable": 8}

var statusNames = func() map[int]string {
	m := map[int]string{}
	for k, v := range statusCodes {
		m[v] = k
	}
	return m
}()

const (
	flagHard     = 1
	flagExcluded = 2
	verdictShift = 4 // verdict kind in bits 4-5
)

var verdictCodes = map[string]int{"ok": 0, "partial": 1, "critical": 2, "unknown": 3}

// migrateHistoryV2 moves the first version's verdicts and hourly
// aggregates, keyed by monitor name, to the tables keyed by id. It runs
// once, in one transaction per database.
func (s *Store) migrateHistoryV2() error {
	var n int
	if err := s.hist.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='verdicts'`).Scan(&n); err != nil || n == 0 {
		if err == nil && n == 0 {
			return s.initAgg5mSince()
		}
		return err
	}
	// Every name in the old history gets an id; the configuration retires
	// the ones it no longer has.
	rows, err := s.hist.Query(`SELECT DISTINCT monitor FROM agg1h UNION SELECT DISTINCT monitor FROM verdicts`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	rows.Close()
	ids := map[string]int64{}
	for _, name := range names {
		id, err := s.idFor(name)
		if err != nil {
			return fmt.Errorf("history migration: %w", err)
		}
		ids[name] = id
	}
	tx, err := s.hist.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TEMP TABLE idmap (name TEXT PRIMARY KEY, mid INTEGER NOT NULL)`); err != nil {
		return err
	}
	for name, id := range ids {
		if _, err := tx.Exec(`INSERT INTO idmap(name, mid) VALUES(?,?)`, name, id); err != nil {
			return err
		}
	}
	stCase := "CASE v.status"
	for name, code := range statusCodes {
		stCase += fmt.Sprintf(" WHEN '%s' THEN %d", name, code)
	}
	stCase += " ELSE 4 END"
	vCase := "CASE v.verdict"
	for name, code := range verdictCodes {
		vCase += fmt.Sprintf(" WHEN '%s' THEN %d", name, code<<verdictShift)
	}
	vCase += " ELSE 0 END"
	for _, q := range []string{
		`INSERT INTO hourly(mid,t,up_s,down_s,ex_s,deg_s,lat_sum,lat_n,lat_max)
			SELECT m.mid, a.hour, a.up_s, a.down_s, a.excluded_s, 0, a.lat_sum, a.lat_n, a.lat_max FROM agg1h a JOIN idmap m ON m.name=a.monitor`,
		`INSERT INTO rounds(mid,at,st,flags,lat,reason,nodes)
			SELECT m.mid, v.at, ` + stCase + `, v.hard*` + strconv.Itoa(flagHard) + ` + v.excluded*` + strconv.Itoa(flagExcluded) + ` + ` + vCase + `,
			NULLIF(v.latency_ms,0), NULLIF(v.reason,''), v.fail_nodes FROM verdicts v JOIN idmap m ON m.name=v.monitor ORDER BY v.at`,
		`DROP TABLE verdicts`,
		`DROP TABLE agg1h`,
		`DROP TABLE idmap`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("history migration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(ids) > 0 {
		s.log.Info("history moved to the per-monitor-id tables", "monitors", len(ids))
	}
	return s.initAgg5mSince()
}

// agg5mSince is when the 5-minute aggregates start; before it, uptime
// comes from the hourly ones only.
func (s *Store) initAgg5mSince() error {
	if v, _ := s.Setting("agg5m_since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			s.agg5mSince = time.Unix(n, 0)
		}
		return nil
	}
	s.agg5mSince = s.now()
	return s.SetSetting("agg5m_since", strconv.FormatInt(s.agg5mSince.Unix(), 10))
}

// aggRow is one bucket of an aggregate table.
type aggRow struct {
	t                         time.Time
	up, down, ex, deg, latSum float64
	latN                      int64
	latMax                    float64
}

func (s *Store) aggRows(table string, mid int64, from, to time.Time) ([]aggRow, error) {
	rows, err := s.hist.Query(`SELECT t,up_s,down_s,ex_s,deg_s,lat_sum,lat_n,lat_max FROM `+table+` WHERE mid=? AND t>=? AND t<? ORDER BY t`,
		mid, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []aggRow
	for rows.Next() {
		var r aggRow
		var t int64
		if err := rows.Scan(&t, &r.up, &r.down, &r.ex, &r.deg, &r.latSum, &r.latN, &r.latMax); err != nil {
			return nil, err
		}
		r.t = time.Unix(t, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// fineFrom is the start of the window the 5-minute aggregates cover,
// rounded up to the hour so the two tables never overlap.
func (s *Store) fineFrom() time.Time {
	start := s.now().Add(-s.retention().Agg5m)
	if s.agg5mSince.After(start) {
		start = s.agg5mSince
	}
	if t := start.Truncate(time.Hour); !t.Equal(start) {
		start = t.Add(time.Hour)
	}
	return start
}

// buckets returns the aggregate rows of [from, to): hourly before the
// 5-minute window, 5-minute within it.
func (s *Store) buckets(mid int64, from, to time.Time) (rows []aggRow, width func(aggRow) time.Duration, err error) {
	fine := s.fineFrom()
	if from.Before(fine) {
		hr, err := s.aggRows("hourly", mid, from.Truncate(time.Hour), minTime(fine, to))
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, hr...)
	}
	if to.After(fine) {
		start := maxTime(fine, from).Truncate(5 * time.Minute)
		fr, err := s.aggRows("agg5m", mid, start, to)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, fr...)
	}
	width = func(r aggRow) time.Duration {
		if r.t.Before(fine) {
			return time.Hour
		}
		return 5 * time.Minute
	}
	return rows, width, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
