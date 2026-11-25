package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"github.com/githubflyideas/unagikuma/internal/engine"
)

// MigrateIndependentSite keeps old evidence and IDs, but never interprets a
// cluster verdict as a new local observation or sends a synthetic recovery.
func (s *Store) MigrateIndependentSite(source string, clustered bool) error {
	if done, _ := s.Setting("per_site_schema"); done == "1" {
		return nil
	}
	epoch, err := s.DBEpoch()
	if err != nil {
		return err
	}
	incs, err := s.queryIncidents("", nil)
	if err != nil {
		return err
	}
	pending, err := s.PendingIntents()
	if err != nil {
		return err
	}
	m := &siteMigration{source: source, epoch: epoch, clustered: clustered, archived: map[int64]bool{}, keys: map[int64]string{}}
	if err := s.inCoreTx(func(tx *sql.Tx) error { m.tx = tx; return m.run(incs, pending) }); err != nil {
		return err
	}
	if _, err := s.hist.Exec("UPDATE rounds SET source_epoch=? WHERE site='' AND source_epoch=''", epoch); err != nil {
		return err
	}
	if m.assigned > 0 || m.dropped > 0 || len(m.archived) > 0 {
		if err := s.Audit(AuditEntry{Actor: "system", Action: "system.per_site_migration", Detail: map[string]any{"source": source, "assigned": m.assigned, "archived_without_recovery": len(m.archived), "dropped_queue": m.dropped}}); err != nil {
			return err
		}
	}
	return s.SetSetting("per_site_schema", "1")
}

// inCoreTx runs f in a write transaction of the core database and commits
// when it returns no error.
func (s *Store) inCoreTx(f func(*sql.Tx) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// siteMigration gives every record of an older release its source site and
// epoch, and archives cluster verdicts that would otherwise look new.
type siteMigration struct {
	tx        *sql.Tx
	source    string
	epoch     string
	clustered bool
	archived  map[int64]bool   // incidents closed without a recovery
	keys      map[int64]string // incident id → global incident key
	assigned  int
	dropped   int
}

func (m *siteMigration) run(incs []*engine.Incident, pending []*engine.Intent) error {
	for _, inc := range incs {
		if err := m.incident(inc); err != nil {
			return err
		}
	}
	for _, in := range pending {
		if err := m.intent(in); err != nil {
			return err
		}
	}
	for id := range m.archived {
		if _, err := m.tx.Exec("DELETE FROM monitor_state WHERE incident_id=?", id); err != nil {
			return err
		}
	}
	for _, table := range []string{"events", "decisions", "deliveries", "evidence", "acks"} {
		if _, err := m.tx.Exec("UPDATE "+table+" SET source_epoch=? WHERE site='' AND source_epoch=''", m.epoch); err != nil {
			return err
		}
	}
	return nil
}

func (m *siteMigration) incident(inc *engine.Incident) error {
	var site string
	if err := m.tx.QueryRow("SELECT site FROM incidents WHERE id=?", inc.ID).Scan(&site); err != nil {
		return err
	}
	legacy := inc.SourceEpoch == ""
	if legacy {
		m.assignSource(inc, site)
	}
	if inc.OriginalID == 0 {
		inc.OriginalID = inc.ID
	}
	m.keys[inc.ID] = engine.IncidentKey(inc.SourceNode, inc.SourceEpoch, inc.OriginalID)
	if site == "" && !inc.State.Closed && m.obsolete(inc, legacy) {
		inc.State.Closed = true
		m.archived[inc.ID] = true
	}
	_, err := m.tx.Exec("UPDATE incidents SET source_node=?,source_epoch=?,source_id=?,state=?,closed=? WHERE id=?", inc.SourceNode, inc.SourceEpoch, inc.OriginalID, mustJSON(inc.State), site != "" || inc.State.Closed, inc.ID)
	return err
}

// assignSource gives an incident from before per-site records its source:
// this server, or the site it was merged from.
func (m *siteMigration) assignSource(inc *engine.Incident, site string) {
	inc.SourceNode = m.source
	inc.SourceEpoch = m.epoch
	if site != "" {
		inc.SourceNode = site
		inc.SourceEpoch = "legacy"
		var origin int64
		_ = m.tx.QueryRow("SELECT origin_id FROM merged WHERE tbl='incidents' AND local_id=? LIMIT 1", inc.ID).Scan(&origin)
		if origin > 0 {
			inc.OriginalID = origin
		}
	}
	m.assigned++
}

// obsolete says whether an open local incident was a cluster verdict, which
// is not this server's own observation.
func (m *siteMigration) obsolete(inc *engine.Incident, legacy bool) bool {
	if inc.Kind == engine.IncidentRegional {
		return true
	}
	if !legacy {
		return false
	}
	return m.clustered && (inc.Kind == engine.IncidentNormal || inc.Kind == engine.IncidentLow) || inc.System == engine.SystemMaster
}

func (m *siteMigration) intent(in *engine.Intent) error {
	if in.Message.GlobalIncidentID == "" {
		in.Message.GlobalIncidentID = m.keys[in.IncidentID]
	}
	obsolete := m.archived[in.IncidentID]
	for _, id := range in.Covers {
		obsolete = obsolete || m.archived[id]
	}
	if obsolete {
		in.State = engine.IntentDropped
		m.dropped++
	}
	_, err := m.tx.Exec("UPDATE intents SET state=?,message=? WHERE id=?", in.State, mustJSON(in.Message), in.ID)
	return err
}

// RotateDBEpoch is used only after a full runtime restore. Historical source
// identities stay unchanged; future observations and incidents use a new epoch.
func (s *Store) RotateDBEpoch(source string) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	old, err := s.DBEpoch()
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"events", "decisions", "deliveries", "evidence", "acks"} {
		if _, err := tx.Exec("UPDATE "+table+" SET source_epoch=?,original_id=id WHERE site='' AND source_epoch=''", old); err != nil {
			return err
		}
	}
	if _, err := s.hist.Exec("UPDATE rounds SET source_epoch=?,original_id=rowid WHERE site='' AND source_epoch=''", old); err != nil {
		return err
	}
	epoch := hex.EncodeToString(b)
	for _, v := range [][2]string{{"db_epoch", epoch}, {"observation_seq", "0"}} {
		if _, err := tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", v[0], v[1]); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("DELETE FROM site_observations WHERE node=?", source); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("restore epoch: %w", err)
	}
	return nil
}
