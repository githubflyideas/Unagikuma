package store

import (
	"crypto/rand"
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
	s.wmu.Lock()
	tx, err := s.core.Begin()
	if err != nil {
		s.wmu.Unlock()
		return err
	}
	fail := func(err error) error { tx.Rollback(); s.wmu.Unlock(); return err }
	archived := map[int64]bool{}
	keys := map[int64]string{}
	assigned := 0
	for _, inc := range incs {
		var site string
		if err := tx.QueryRow("SELECT site FROM incidents WHERE id=?", inc.ID).Scan(&site); err != nil {
			return fail(err)
		}
		legacy := inc.SourceEpoch == ""
		if legacy {
			inc.SourceNode = source
			inc.SourceEpoch = epoch
			if site != "" {
				inc.SourceNode = site
				inc.SourceEpoch = "legacy"
				var origin int64
				_ = tx.QueryRow("SELECT origin_id FROM merged WHERE tbl='incidents' AND local_id=? LIMIT 1", inc.ID).Scan(&origin)
				if origin > 0 {
					inc.OriginalID = origin
				}
			}
			assigned++
		}
		if inc.OriginalID == 0 {
			inc.OriginalID = inc.ID
		}
		keys[inc.ID] = engine.IncidentKey(inc.SourceNode, inc.SourceEpoch, inc.OriginalID)
		obsolete := site == "" && !inc.State.Closed && (inc.Kind == engine.IncidentRegional || (legacy && clustered && (inc.Kind == engine.IncidentNormal || inc.Kind == engine.IncidentLow)) || (legacy && inc.System == engine.SystemMaster))
		if obsolete {
			inc.State.Closed = true
			archived[inc.ID] = true
		}
		if _, err := tx.Exec("UPDATE incidents SET source_node=?,source_epoch=?,source_id=?,state=?,closed=? WHERE id=?", inc.SourceNode, inc.SourceEpoch, inc.OriginalID, mustJSON(inc.State), site != "" || inc.State.Closed, inc.ID); err != nil {
			return fail(err)
		}
	}
	dropped := 0
	for _, in := range pending {
		if in.Message.GlobalIncidentID == "" {
			in.Message.GlobalIncidentID = keys[in.IncidentID]
		}
		obsolete := archived[in.IncidentID]
		for _, id := range in.Covers {
			obsolete = obsolete || archived[id]
		}
		if obsolete {
			in.State = engine.IntentDropped
			dropped++
		}
		if _, err := tx.Exec("UPDATE intents SET state=?,message=? WHERE id=?", in.State, mustJSON(in.Message), in.ID); err != nil {
			return fail(err)
		}
	}
	for id := range archived {
		if _, err := tx.Exec("DELETE FROM monitor_state WHERE incident_id=?", id); err != nil {
			return fail(err)
		}
	}
	for _, table := range []string{"events", "decisions", "deliveries", "evidence", "acks"} {
		if _, err := tx.Exec("UPDATE "+table+" SET source_epoch=? WHERE site='' AND source_epoch=''", epoch); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		s.wmu.Unlock()
		return err
	}
	s.wmu.Unlock()
	if _, err := s.hist.Exec("UPDATE rounds SET source_epoch=? WHERE site='' AND source_epoch=''", epoch); err != nil {
		return err
	}
	if assigned > 0 || dropped > 0 || len(archived) > 0 {
		if err := s.Audit(AuditEntry{Actor: "system", Action: "system.per_site_migration", Detail: map[string]any{"source": source, "assigned": assigned, "archived_without_recovery": len(archived), "dropped_queue": dropped}}); err != nil {
			return err
		}
	}
	return s.SetSetting("per_site_schema", "1")
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
