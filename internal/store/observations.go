package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/githubflyideas/unagikuma/internal/observation"
	"sort"
	"strconv"
	"time"
)

// DBEpoch survives restarts and promotion, but not a newly initialized DB.
func (s *Store) DBEpoch() (string, error) {
	var epoch string
	if err := s.core.QueryRow("SELECT value FROM settings WHERE key='db_epoch'").Scan(&epoch); err == nil {
		return epoch, nil
	} else if err != sql.ErrNoRows {
		return "", err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	if _, err := s.core.Exec("INSERT OR IGNORE INTO settings(key,value) VALUES('db_epoch',?)", hex.EncodeToString(b)); err != nil {
		return "", err
	}
	err := s.core.QueryRow("SELECT value FROM settings WHERE key='db_epoch'").Scan(&epoch)
	return epoch, err
}
func (s *Store) RecordSiteObservation(o observation.Row) error {
	epoch, err := s.DBEpoch()
	if err != nil {
		return err
	}
	o.Epoch = epoch
	o.ReceivedAt = s.now()
	o.AgeMS = 0
	o.Stale = false
	if len(o.Error) > 2048 {
		o.Error = o.Error[:2048]
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old string
	err = tx.QueryRow("SELECT data FROM site_observations WHERE node=? AND monitor=?", o.NodeID, o.MonitorID).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var prev observation.Row
	if json.Unmarshal([]byte(old), &prev) == nil && prev.Epoch == o.Epoch && prev.Status == o.Status && prev.DefHash == o.DefHash {
		o.StatusSince = prev.StatusSince
	} else {
		o.StatusSince = o.CheckedAt
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO settings(key,value) VALUES('observation_seq','0')"); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE settings SET value=CAST(value AS INTEGER)+1 WHERE key='observation_seq'"); err != nil {
		return err
	}
	var seq string
	if err := tx.QueryRow("SELECT value FROM settings WHERE key='observation_seq'").Scan(&seq); err != nil {
		return err
	}
	o.Seq, err = strconv.ParseInt(seq, 10, 64)
	if err != nil {
		return err
	}
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO site_observations(node,monitor,epoch,seq,data) VALUES(?,?,?,?,?) ON CONFLICT(node,monitor) DO UPDATE SET epoch=excluded.epoch,seq=excluded.seq,data=excluded.data", o.NodeID, o.MonitorID, o.Epoch, o.Seq, string(b)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SiteObservationDelta(node, epoch string, after int64) (observation.Delta, error) {
	own, err := s.DBEpoch()
	d := observation.Delta{NodeID: node, Epoch: own, Rows: []observation.Row{}}
	if err != nil {
		return d, err
	}
	if epoch != own {
		after = 0
	}
	d.Cursor = after
	rows, err := s.core.Query("SELECT data FROM site_observations WHERE node=? AND epoch=? AND seq>? ORDER BY seq LIMIT 257", node, own, after)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	now := s.now()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return d, err
		}
		var o observation.Row
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			return d, err
		}
		if len(d.Rows) == 256 {
			d.More = true
			break
		}
		o.AgeMS = min(int64(365*24*time.Hour/time.Millisecond), max(int64(0), now.Sub(o.CheckedAt).Milliseconds()))
		if o.CheckedAt.After(now.Add(time.Minute)) {
			o.AgeMS = max(o.IntervalMS*4, int64(time.Hour/time.Millisecond))
		}
		d.Rows = append(d.Rows, o)
		d.Cursor = o.Seq
	}
	return d, rows.Err()
}
func (s *Store) SiteObservationCursor(node string) (string, int64) {
	var epoch string
	var seq int64
	_ = s.core.QueryRow("SELECT epoch,seq FROM site_observation_cursors WHERE node=?", node).Scan(&epoch, &seq)
	return epoch, seq
}

// ApplySiteObservationDelta accepts only a directly authenticated source's rows.
func (s *Store) ApplySiteObservationDelta(node string, d observation.Delta) error {
	epoch, err := hex.DecodeString(d.Epoch)
	if err != nil || len(epoch) != 16 || d.NodeID != node || len(d.Rows) > 256 || d.Cursor < 0 {
		return fmt.Errorf("invalid observation source")
	}
	prevSeq := int64(0)
	for _, o := range d.Rows {
		if o.NodeID != node || o.Epoch != d.Epoch || o.Seq <= prevSeq || o.Seq > d.Cursor || o.IntervalMS <= 0 || o.AgeMS < 0 || o.AgeMS > int64(365*24*time.Hour/time.Millisecond) || o.MonitorID == "" || len(o.Error) > 2048 {
			return fmt.Errorf("invalid observation row")
		}
		if o.Status != "up" && o.Status != "down" && o.Status != "unknown" {
			return fmt.Errorf("invalid observation status")
		}
		prevSeq = o.Seq
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldEpoch string
	var oldSeq int64
	err = tx.QueryRow("SELECT epoch,seq FROM site_observation_cursors WHERE node=?", node).Scan(&oldEpoch, &oldSeq)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if oldEpoch == d.Epoch && d.Cursor < oldSeq {
		return fmt.Errorf("observation cursor regressed")
	}
	if oldEpoch != d.Epoch {
		if _, err := tx.Exec("DELETE FROM site_observations WHERE node=?", node); err != nil {
			return err
		}
		oldSeq = 0
	}
	for _, o := range d.Rows {
		if o.Seq <= oldSeq {
			continue
		}
		o.ReceivedAt = s.now()
		o.Stale = false
		b, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO site_observations(node,monitor,epoch,seq,data) VALUES(?,?,?,?,?) ON CONFLICT(node,monitor) DO UPDATE SET epoch=excluded.epoch,seq=excluded.seq,data=excluded.data", node, o.MonitorID, o.Epoch, o.Seq, string(b)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT INTO site_observation_cursors(node,epoch,seq) VALUES(?,?,?) ON CONFLICT(node) DO UPDATE SET epoch=excluded.epoch,seq=excluded.seq", node, d.Epoch, d.Cursor); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SiteObservationSnapshot(monitor, defhash string, sites []string, now time.Time) (*observation.Snapshot, error) {
	if len(sites) == 0 {
		return nil, nil
	}
	rows, err := s.core.Query("SELECT data FROM site_observations WHERE monitor=?", monitor)
	if err != nil {
		return nil, err
	}
	have := map[string]observation.Row{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var o observation.Row
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			rows.Close()
			return nil, err
		}
		if o.DefHash == defhash {
			have[o.NodeID] = o
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := &observation.Snapshot{TakenAt: now, Rows: []observation.Row{}}
	names := append([]string(nil), sites...)
	sort.Strings(names)
	for _, site := range names {
		o, ok := have[site]
		if !ok {
			o = observation.Row{NodeID: site, MonitorID: monitor, Status: "unknown", Stale: true}
		} else {
			age := o.AgeMS + max(int64(0), now.Sub(o.ReceivedAt).Milliseconds())
			o.Stale = o.IntervalMS <= 0 || age > 3*o.IntervalMS || o.ReceivedAt.After(now.Add(time.Second))
		}
		out.Rows = append(out.Rows, o)
	}
	return out, nil
}

func (s *Store) PruneObservationConfig(monitors map[string]string, sites map[string]bool) error {
	rows, err := s.core.Query("SELECT node,monitor,data FROM site_observations")
	if err != nil {
		return err
	}
	type key struct{ node, monitor string }
	obsolete := []key{}
	for rows.Next() {
		var node, monitor, data string
		if err := rows.Scan(&node, &monitor, &data); err != nil {
			rows.Close()
			return err
		}
		var o observation.Row
		_ = json.Unmarshal([]byte(data), &o)
		if hash, ok := monitors[monitor]; !ok || !sites[node] || hash != o.DefHash {
			obsolete = append(obsolete, key{node, monitor})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, k := range obsolete {
		if _, err := s.core.Exec("DELETE FROM site_observations WHERE node=? AND monitor=?", k.node, k.monitor); err != nil {
			return err
		}
	}
	return nil
}
