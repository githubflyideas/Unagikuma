package store

import (
	"database/sql"
	"errors"
	"sync"
)

// Monitors have a stable numeric id. The configuration names them, so the
// id follows the name: a rename carries it (and the history keyed by it)
// over, a deleted monitor's id is retired, and a monitor that comes back
// with the same definition (a rollback) gets its old id and history back.

// MonitorDef is what SyncMonitors needs of a configured monitor.
type MonitorDef struct {
	Name    string
	DefHash string
}

type monitorIDs struct {
	mu      sync.Mutex
	byName  map[string]int64
	aliases map[string]string // old name → new name, for rounds still queued under the old one
}

// MonitorID returns the id of an active monitor (0 when unknown).
func (s *Store) MonitorID(name string) int64 {
	s.ids.mu.Lock()
	defer s.ids.mu.Unlock()
	return s.ids.byName[s.ids.resolve(name)]
}

// MonitorName returns the name of the active monitor with this id ("" when
// none is).
func (s *Store) MonitorName(id int64) string {
	s.ids.mu.Lock()
	defer s.ids.mu.Unlock()
	for name, v := range s.ids.byName {
		if v == id {
			return name
		}
	}
	return ""
}

// MonitorIDs returns the ids of all active monitors.
func (s *Store) MonitorIDs() map[string]int64 {
	s.ids.mu.Lock()
	defer s.ids.mu.Unlock()
	out := make(map[string]int64, len(s.ids.byName))
	for k, v := range s.ids.byName {
		out[k] = v
	}
	return out
}

func (m *monitorIDs) resolve(name string) string {
	for i := 0; i < 8; i++ { // chains of renames; bounded against loops
		next, ok := m.aliases[name]
		if !ok {
			break
		}
		name = next
	}
	return name
}

func (s *Store) loadMonitorIDs() error {
	rows, err := s.core.Query(`SELECT id, name FROM monitors WHERE retired_at IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.ids.byName, s.ids.aliases = map[string]int64{}, map[string]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		s.ids.byName[name] = id
	}
	return rows.Err()
}

// idFor returns the id of a monitor, creating it when the name is new
// (rounds can arrive before the configuration that names it is synced).
func (s *Store) idFor(name string) (int64, error) {
	s.ids.mu.Lock()
	name = s.ids.resolve(name)
	id, ok := s.ids.byName[name]
	s.ids.mu.Unlock()
	if ok {
		return id, nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err = activeOrNew(tx, name, "", ms(s.now()))
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.ids.mu.Lock()
	s.ids.byName[name] = id
	s.ids.mu.Unlock()
	return id, nil
}

// activeOrNew returns the active id of name, reviving a retired one with
// the same definition, or inserting a new one.
func activeOrNew(tx *sql.Tx, name, hash string, now int64) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM monitors WHERE name=? AND retired_at IS NULL`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if hash != "" {
		err = tx.QueryRow(`SELECT id FROM monitors WHERE name=? AND def_hash=? AND retired_at IS NOT NULL ORDER BY retired_at DESC LIMIT 1`, name, hash).Scan(&id)
		if err == nil {
			_, err = tx.Exec(`UPDATE monitors SET retired_at=NULL WHERE id=?`, id)
			return id, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	res, err := tx.Exec(`INSERT INTO monitors(name, def_hash, created_at) VALUES(?,?,?)`, name, hash, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SyncMonitors makes the active monitors those of the configuration:
// new names get an id (or their retired one back), missing ones retire.
func (s *Store) SyncMonitors(defs []MonitorDef) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ms(s.now())
	active := map[string]int64{}
	hashes := map[string]string{}
	rows, err := tx.Query(`SELECT id, name, def_hash FROM monitors WHERE retired_at IS NULL`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var name, hash string
		if err := rows.Scan(&id, &name, &hash); err != nil {
			rows.Close()
			return err
		}
		active[name], hashes[name] = id, hash
	}
	rows.Close()
	ids := make(map[string]int64, len(defs))
	for _, d := range defs {
		if id, ok := active[d.Name]; ok {
			if hashes[d.Name] != d.DefHash {
				if _, err := tx.Exec(`UPDATE monitors SET def_hash=? WHERE id=?`, d.DefHash, id); err != nil {
					return err
				}
			}
			ids[d.Name] = id
			delete(active, d.Name)
			continue
		}
		id, err := activeOrNew(tx, d.Name, d.DefHash, now)
		if err != nil {
			return err
		}
		ids[d.Name] = id
	}
	for _, id := range active {
		if _, err := tx.Exec(`UPDATE monitors SET retired_at=? WHERE id=?`, now, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.ids.mu.Lock()
	s.ids.byName = ids
	s.ids.mu.Unlock()
	return nil
}

// RenameMonitor moves a monitor to a new name: its id, open incidents,
// events, notification log, evidence and push token follow.
func (s *Store) RenameMonitor(oldName, newName string) error {
	s.batch.flushNow()
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.core.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ms(s.now())
	// A monitor already active under the new name is replaced.
	if _, err := tx.Exec(`UPDATE monitors SET retired_at=? WHERE name=? AND retired_at IS NULL`, now, newName); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE monitors SET name=? WHERE name=? AND retired_at IS NULL`,
		`UPDATE incidents SET monitor=? WHERE monitor=?`,
		`UPDATE events SET monitor=? WHERE monitor=?`,
		`UPDATE deliveries SET monitor=? WHERE monitor=?`,
		`UPDATE decisions SET monitor=? WHERE monitor=?`,
		`UPDATE evidence SET monitor=? WHERE monitor=?`,
		`UPDATE push_tokens SET monitor=? WHERE monitor=?`,
	} {
		if _, err := tx.Exec(q, newName, oldName); err != nil {
			return err
		}
	}
	// The push URL stays the same.
	for _, k := range []string{"push_plain:", "push_gen:"} {
		if _, err := tx.Exec(`DELETE FROM settings WHERE key=?`, k+newName); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE settings SET key=? WHERE key=?`, k+newName, k+oldName); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM monitor_state WHERE name=?`, newName); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE monitor_state SET name=? WHERE name=?`, newName, oldName); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.ids.mu.Lock()
	if id, ok := s.ids.byName[oldName]; ok {
		delete(s.ids.byName, oldName)
		s.ids.byName[newName] = id
	}
	s.ids.aliases[oldName] = newName
	delete(s.ids.aliases, newName)
	s.ids.mu.Unlock()
	return nil
}
