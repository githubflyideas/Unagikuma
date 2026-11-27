package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// schemaEpoch is the first user_version of the 3.0.1 squashed schema.
// Releases before 3.0.1 used 1–29 for core.db and 1–6 for history.db;
// those databases cannot be upgraded and must be replaced with a fresh
// data directory. Future migrations append to the slices below and
// receive user_version schemaEpoch, schemaEpoch+1, …
const schemaEpoch = 1001

// coreMigrations is the core.db schema. The first entry is the complete
// current schema; later entries are incremental, each run once.
var coreMigrations = []string{
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, password TEXT NOT NULL,
		created_at INTEGER NOT NULL, prefs TEXT, role TEXT NOT NULL DEFAULT 'admin', areas TEXT NOT NULL DEFAULT '',
		disabled INTEGER NOT NULL DEFAULT 0, groups TEXT NOT NULL DEFAULT '');
	CREATE TABLE sessions (id_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
	CREATE TABLE tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, prefix TEXT NOT NULL, hash TEXT NOT NULL UNIQUE,
		scopes TEXT NOT NULL, user_id INTEGER, created_at INTEGER NOT NULL, last_used INTEGER);
	CREATE TABLE secrets (name TEXT PRIMARY KEY, key_id TEXT NOT NULL, dek TEXT NOT NULL, value TEXT NOT NULL,
		scopes TEXT, updated_at INTEGER NOT NULL);
	CREATE TABLE monitor_state (name TEXT PRIMARY KEY, state TEXT NOT NULL, status TEXT NOT NULL, reason TEXT, detail TEXT,
		latency_ms REAL, last_check INTEGER, last_change INTEGER, incident_id INTEGER, def_hash TEXT);
	CREATE TABLE monitors (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, def_hash TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL, retired_at INTEGER);
	CREATE UNIQUE INDEX monitors_active ON monitors(name) WHERE retired_at IS NULL;
	CREATE TABLE incidents (id INTEGER PRIMARY KEY AUTOINCREMENT, monitor TEXT NOT NULL, kind TEXT NOT NULL, system TEXT,
		policy TEXT, reason TEXT, detail TEXT, title TEXT, ack_comment TEXT, summary TEXT, state TEXT NOT NULL,
		opened_at INTEGER NOT NULL, resolved_at INTEGER, closed INTEGER NOT NULL DEFAULT 0, acked INTEGER NOT NULL DEFAULT 0,
		drill INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL, site TEXT NOT NULL DEFAULT '', term INTEGER NOT NULL DEFAULT 0,
		source_node TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', source_id INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX incidents_monitor ON incidents(monitor, opened_at);
	CREATE INDEX incidents_open ON incidents(closed, resolved_at);
	CREATE INDEX incidents_source ON incidents(source_node,source_epoch,source_id);
	CREATE TABLE intents (id TEXT PRIMARY KEY, incident_id INTEGER, seq INTEGER, kind TEXT NOT NULL, policy TEXT,
		message TEXT NOT NULL, delivery TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, due_at INTEGER,
		updated_at INTEGER, system INTEGER NOT NULL DEFAULT 0, force_fail INTEGER NOT NULL DEFAULT 0, attempts TEXT,
		covers TEXT NOT NULL DEFAULT '', chain TEXT NOT NULL DEFAULT '');
	CREATE INDEX intents_state ON intents(state, created_at);
	CREATE TABLE deliveries (id INTEGER PRIMARY KEY AUTOINCREMENT, intent_id TEXT, incident_id INTEGER, kind TEXT, seq INTEGER,
		at INTEGER NOT NULL, monitor TEXT, title TEXT, attempts TEXT, ok INTEGER, fallback INTEGER, drill INTEGER, delayed_s INTEGER,
		site TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', original_id INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX deliveries_at ON deliveries(at);
	CREATE INDEX deliveries_source ON deliveries(site,source_epoch,original_id);
	CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, type TEXT NOT NULL, severity TEXT,
		monitor TEXT, message TEXT, data TEXT, site TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', original_id INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX events_at ON events(at);
	CREATE INDEX events_monitor ON events(monitor, id);
	CREATE INDEX events_source ON events(site,source_epoch,original_id);
	CREATE TABLE config_snapshots (serial INTEGER PRIMARY KEY, at INTEGER NOT NULL, source TEXT, author TEXT, message TEXT,
		text TEXT NOT NULL, sha256 TEXT NOT NULL, summary TEXT, term INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE counters (day TEXT NOT NULL, name TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (day, name));
	CREATE TABLE push_tokens (hash TEXT PRIMARY KEY, monitor TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL);
	CREATE TABLE evidence (id INTEGER PRIMARY KEY AUTOINCREMENT, monitor TEXT NOT NULL, node TEXT NOT NULL, at INTEGER NOT NULL,
		result TEXT NOT NULL, site TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', original_id INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX evidence_monitor ON evidence(monitor, node, id);
	CREATE INDEX evidence_source ON evidence(site,source_epoch,original_id);
	CREATE TABLE agent_tokens (node TEXT PRIMARY KEY, prefix TEXT NOT NULL, hash TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL, last_used INTEGER, last_addr TEXT);
	CREATE TABLE join_codes (hash TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
		used_at INTEGER, node TEXT, rejoin TEXT NOT NULL DEFAULT '');
	CREATE TABLE audit (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '', area TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', addr TEXT NOT NULL DEFAULT '',
		risk TEXT NOT NULL DEFAULT '', prev TEXT NOT NULL DEFAULT '', hash TEXT NOT NULL DEFAULT '');
	CREATE INDEX audit_at ON audit(at);
	CREATE INDEX audit_action ON audit(action);
	CREATE TRIGGER audit_append_only BEFORE UPDATE ON audit WHEN OLD.hash != ''
	BEGIN SELECT RAISE(ABORT, 'the audit log is append-only'); END;
	CREATE TABLE announcements (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL,
		body TEXT NOT NULL DEFAULT '', severity TEXT NOT NULL, author TEXT NOT NULL DEFAULT '',
		pinned INTEGER NOT NULL DEFAULT 0, starts_at INTEGER, ends_at INTEGER,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, resolved_at INTEGER);
	CREATE INDEX announcements_created ON announcements(pinned DESC, created_at DESC);
	CREATE TABLE subscribers (id INTEGER PRIMARY KEY AUTOINCREMENT, page TEXT NOT NULL, email TEXT NOT NULL,
		token TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, confirmed_at INTEGER, UNIQUE (page, email));
	CREATE TABLE acks (id INTEGER PRIMARY KEY AUTOINCREMENT, incident_id INTEGER NOT NULL, at INTEGER NOT NULL,
		by TEXT NOT NULL, via TEXT NOT NULL, action TEXT NOT NULL, comment TEXT NOT NULL DEFAULT '',
		site TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', original_id INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX acks_incident ON acks(incident_id, id);
	CREATE INDEX acks_source ON acks(site,source_epoch,original_id);
	CREATE TABLE people (id INTEGER PRIMARY KEY AUTOINCREMENT, handle TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
		note TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '', left_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, notify TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '', no_report INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE contacts (id INTEGER PRIMARY KEY AUTOINCREMENT, person_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		kind TEXT NOT NULL, value TEXT NOT NULL, UNIQUE (kind, value));
	CREATE TABLE bounces (kind TEXT NOT NULL, value TEXT NOT NULL, n INTEGER NOT NULL, first_at INTEGER NOT NULL,
		last_at INTEGER NOT NULL, reason TEXT NOT NULL, PRIMARY KEY (kind, value));
	CREATE TABLE decisions (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, incident_id INTEGER NOT NULL DEFAULT 0,
		intent_id TEXT NOT NULL DEFAULT '', monitor TEXT NOT NULL DEFAULT '', step TEXT NOT NULL, data TEXT NOT NULL,
		site TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', original_id INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX decisions_at ON decisions(at);
	CREATE INDEX decisions_incident ON decisions(incident_id);
	CREATE INDEX decisions_monitor_at ON decisions(monitor, at);
	CREATE INDEX decisions_source ON decisions(site,source_epoch,original_id);
	CREATE TABLE merge_marks (site TEXT NOT NULL, tbl TEXT NOT NULL, last_id INTEGER NOT NULL, PRIMARY KEY (site, tbl));
	CREATE TABLE merged (site TEXT NOT NULL, tbl TEXT NOT NULL, origin_id INTEGER NOT NULL, local_id INTEGER NOT NULL,
		PRIMARY KEY (site, tbl, origin_id));
	CREATE TABLE diverged_configs (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, site TEXT NOT NULL,
		term INTEGER NOT NULL, serial INTEGER NOT NULL, sha256 TEXT NOT NULL, text TEXT NOT NULL, base_serial INTEGER NOT NULL DEFAULT 0,
		UNIQUE(site, sha256));
	CREATE TABLE site_observations(node TEXT NOT NULL,monitor TEXT NOT NULL,epoch TEXT NOT NULL,seq INTEGER NOT NULL,data TEXT NOT NULL,PRIMARY KEY(node,monitor));
	CREATE INDEX site_observations_delta ON site_observations(node,epoch,seq);
	CREATE TABLE site_audit (site TEXT NOT NULL, term INTEGER NOT NULL DEFAULT 0, origin_id INTEGER NOT NULL, at INTEGER NOT NULL,
		actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL DEFAULT '', area TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '', addr TEXT NOT NULL DEFAULT '', risk TEXT NOT NULL DEFAULT '',
		prev TEXT NOT NULL DEFAULT '', hash TEXT NOT NULL DEFAULT '', PRIMARY KEY (site, origin_id));
	CREATE INDEX site_audit_at ON site_audit(at);
	CREATE TABLE site_observation_cursors(node TEXT PRIMARY KEY,epoch TEXT NOT NULL,seq INTEGER NOT NULL);
	CREATE TABLE saved_alerts (at INTEGER NOT NULL, name TEXT NOT NULL, monitor TEXT NOT NULL, n INTEGER NOT NULL);
	CREATE INDEX saved_alerts_at ON saved_alerts(at);
	CREATE TABLE value_samples (monitor TEXT NOT NULL, at INTEGER NOT NULL, v REAL NOT NULL, def_hash TEXT NOT NULL DEFAULT '');
	CREATE INDEX value_samples_monitor_at ON value_samples(monitor, at);`,
}

// historyMigrations is the history.db schema. The first entry is the
// complete current schema (per-monitor-id rounds and aggregates).
var historyMigrations = []string{
	`CREATE TABLE rounds (id INTEGER PRIMARY KEY AUTOINCREMENT, mid INTEGER NOT NULL, at INTEGER NOT NULL, st INTEGER NOT NULL, flags INTEGER NOT NULL,
		lat REAL, reason TEXT, nodes TEXT, site TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', original_id INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX rounds_mid_at ON rounds(mid,at);
	CREATE INDEX rounds_at ON rounds(at);
	CREATE INDEX rounds_source ON rounds(site,source_epoch,original_id);
	CREATE TABLE agg5m (mid INTEGER NOT NULL, t INTEGER NOT NULL, up_s REAL NOT NULL, down_s REAL NOT NULL, ex_s REAL NOT NULL,
		deg_s REAL NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, lat_max REAL NOT NULL, PRIMARY KEY (mid, t)) WITHOUT ROWID;
	CREATE TABLE hourly (mid INTEGER NOT NULL, t INTEGER NOT NULL, up_s REAL NOT NULL, down_s REAL NOT NULL, ex_s REAL NOT NULL,
		deg_s REAL NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, lat_max REAL NOT NULL, PRIMARY KEY (mid, t)) WITHOUT ROWID;
	CREATE TABLE merge_marks (site TEXT NOT NULL, tbl TEXT NOT NULL, last_id INTEGER NOT NULL, PRIMARY KEY (site, tbl));`,
}

func (s *Store) migrate() error {
	if err := migrateDB(s, "core", coreMigrations); err != nil {
		return err
	}
	if err := migrateDB(s, "history", historyMigrations); err != nil {
		return err
	}
	if err := s.loadMonitorIDs(); err != nil {
		return err
	}
	return s.migrateHistoryV2()
}

func migrateDB(s *Store, name string, migrations []string) error {
	db := s.core
	if name == "history" {
		db = s.hist
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	current := schemaEpoch + len(migrations) - 1
	switch {
	case version == 0:
		// A new, empty database.
	case version < schemaEpoch:
		return fmt.Errorf("%s.db is from before 3.0.1 (schema %d) and cannot be upgraded; start unagikuma with a fresh data directory", name, version)
	case version > current:
		return fmt.Errorf("%s.db was written by a newer unagikuma (schema %d > %d)", name, version, current)
	}
	if version >= schemaEpoch && version < current {
		backup := filepath.Join(s.dir, fmt.Sprintf("%s.db.bak-%d", name, version))
		if _, err := os.Stat(backup); err != nil {
			if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
				return fmt.Errorf("backup before migration: %w", err)
			}
		}
	}
	start := 0
	if version >= schemaEpoch {
		start = version - schemaEpoch + 1
	}
	for i := start; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s migration %d: %w", name, schemaEpoch+i, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaEpoch+i)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// CheckSchema says whether the databases in dir (core.db, history.db) can
// be opened by this version: a database from before 3.0.1 cannot be
// upgraded and one from a newer version cannot be read. A missing
// database is fine. Nothing in dir is changed.
func CheckSchema(dir string) error {
	for _, c := range []struct {
		name       string
		migrations []string
	}{{"core", coreMigrations}, {"history", historyMigrations}} {
		path := filepath.Join(dir, c.name+".db")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			return fmt.Errorf("%s.db: %w", c.name, err)
		}
		var version int
		err = db.QueryRow(`PRAGMA user_version`).Scan(&version)
		db.Close()
		if err != nil {
			return fmt.Errorf("%s.db is not a readable database: %w", c.name, err)
		}
		current := schemaEpoch + len(c.migrations) - 1
		switch {
		case version == 0:
		case version < schemaEpoch:
			return fmt.Errorf("%s.db is from before 3.0.1 (schema %d) and cannot be upgraded", c.name, version)
		case version > current:
			return fmt.Errorf("%s.db was written by a newer unagikuma (schema %d > %d)", c.name, version, current)
		}
	}
	return nil
}
