package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// Migrations are append-only: each runs once, in a transaction.
var coreMigrations = []string{
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE monitor_state (name TEXT PRIMARY KEY, state TEXT NOT NULL, status TEXT NOT NULL, reason TEXT, detail TEXT,
		latency_ms REAL, last_check INTEGER, last_change INTEGER, incident_id INTEGER, def_hash TEXT);
	CREATE TABLE incidents (id INTEGER PRIMARY KEY AUTOINCREMENT, monitor TEXT NOT NULL, kind TEXT NOT NULL, system TEXT,
		policy TEXT, reason TEXT, detail TEXT, title TEXT, ack_comment TEXT, summary TEXT, state TEXT NOT NULL,
		opened_at INTEGER NOT NULL, resolved_at INTEGER, closed INTEGER NOT NULL DEFAULT 0, acked INTEGER NOT NULL DEFAULT 0,
		drill INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
	CREATE INDEX incidents_monitor ON incidents(monitor, opened_at);
	CREATE INDEX incidents_open ON incidents(closed, resolved_at);
	CREATE TABLE intents (id TEXT PRIMARY KEY, incident_id INTEGER, seq INTEGER, kind TEXT NOT NULL, policy TEXT,
		message TEXT NOT NULL, delivery TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, due_at INTEGER,
		updated_at INTEGER, system INTEGER NOT NULL DEFAULT 0, force_fail INTEGER NOT NULL DEFAULT 0, attempts TEXT);
	CREATE INDEX intents_state ON intents(state, created_at);
	CREATE TABLE deliveries (id INTEGER PRIMARY KEY AUTOINCREMENT, intent_id TEXT, incident_id INTEGER, kind TEXT, seq INTEGER,
		at INTEGER NOT NULL, monitor TEXT, title TEXT, attempts TEXT, ok INTEGER, fallback INTEGER, drill INTEGER, delayed_s INTEGER);
	CREATE INDEX deliveries_at ON deliveries(at);
	CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, type TEXT NOT NULL, severity TEXT,
		monitor TEXT, message TEXT, data TEXT);
	CREATE INDEX events_at ON events(at);
	CREATE INDEX events_monitor ON events(monitor, id);
	CREATE TABLE config_snapshots (serial INTEGER PRIMARY KEY, at INTEGER NOT NULL, source TEXT, author TEXT, message TEXT,
		text TEXT NOT NULL, sha256 TEXT NOT NULL, summary TEXT);
	CREATE TABLE counters (day TEXT NOT NULL, name TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (day, name));
	CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, password TEXT NOT NULL,
		created_at INTEGER NOT NULL, prefs TEXT);
	CREATE TABLE sessions (id_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
	CREATE TABLE tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, prefix TEXT NOT NULL, hash TEXT NOT NULL UNIQUE,
		scopes TEXT NOT NULL, user_id INTEGER, created_at INTEGER NOT NULL, last_used INTEGER);
	CREATE TABLE secrets (name TEXT PRIMARY KEY, key_id TEXT NOT NULL, dek TEXT NOT NULL, value TEXT NOT NULL,
		scopes TEXT, updated_at INTEGER NOT NULL);
	CREATE TABLE push_tokens (hash TEXT PRIMARY KEY, monitor TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL);
	CREATE TABLE evidence (id INTEGER PRIMARY KEY AUTOINCREMENT, monitor TEXT NOT NULL, node TEXT NOT NULL, at INTEGER NOT NULL,
		result TEXT NOT NULL);
	CREATE INDEX evidence_monitor ON evidence(monitor, node, id);`,
	`CREATE TABLE agent_tokens (node TEXT PRIMARY KEY, prefix TEXT NOT NULL, hash TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL, last_used INTEGER, last_addr TEXT);`,
	`CREATE TABLE join_codes (hash TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
		used_at INTEGER, node TEXT);`,
	`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';
	ALTER TABLE users ADD COLUMN areas TEXT NOT NULL DEFAULT '';`,
	`CREATE TABLE audit (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '', area TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', addr TEXT NOT NULL DEFAULT '');
	CREATE INDEX audit_at ON audit(at);`,
	`CREATE TABLE announcements (id INTEGER PRIMARY KEY AUTOINCREMENT, page TEXT NOT NULL, title TEXT NOT NULL,
		body TEXT NOT NULL DEFAULT '', severity TEXT NOT NULL, author TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, resolved_at INTEGER);
	CREATE INDEX announcements_page ON announcements(page, created_at);
	CREATE TABLE subscribers (id INTEGER PRIMARY KEY AUTOINCREMENT, page TEXT NOT NULL, email TEXT NOT NULL,
		token TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, confirmed_at INTEGER, UNIQUE (page, email));`,
	`CREATE TABLE monitors (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, def_hash TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL, retired_at INTEGER);
	CREATE UNIQUE INDEX monitors_active ON monitors(name) WHERE retired_at IS NULL;`,
	// Acknowledgements are appended, never overwritten: who took an
	// incident on, when, through what and with which note.
	`CREATE TABLE acks (id INTEGER PRIMARY KEY AUTOINCREMENT, incident_id INTEGER NOT NULL, at INTEGER NOT NULL,
		by TEXT NOT NULL, via TEXT NOT NULL, action TEXT NOT NULL, comment TEXT NOT NULL DEFAULT '');
	CREATE INDEX acks_incident ON acks(incident_id, id);`,
	// The contact directory: who may receive alerts, at which addresses.
	// Bounces are kept per address whether or not it is in the directory.
	`CREATE TABLE people (id INTEGER PRIMARY KEY AUTOINCREMENT, handle TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
		note TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '', left_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
	CREATE TABLE contacts (id INTEGER PRIMARY KEY AUTOINCREMENT, person_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		kind TEXT NOT NULL, value TEXT NOT NULL, UNIQUE (kind, value));
	CREATE TABLE bounces (kind TEXT NOT NULL, value TEXT NOT NULL, n INTEGER NOT NULL, first_at INTEGER NOT NULL,
		last_at INTEGER NOT NULL, reason TEXT NOT NULL, PRIMARY KEY (kind, value));
	ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;`,
	// Announced changes (planned work): their window, who is told, and
	// which notices already went out, so a restart never repeats one.
	`CREATE TABLE changes (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
		impact TEXT NOT NULL DEFAULT '', sel TEXT NOT NULL DEFAULT '', policy TEXT NOT NULL DEFAULT '', channels TEXT NOT NULL DEFAULT '',
		status_page TEXT NOT NULL DEFAULT '', announcement_id INTEGER NOT NULL DEFAULT 0, start_at INTEGER NOT NULL, end_at INTEGER NOT NULL,
		state TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0, finished_by TEXT NOT NULL DEFAULT '', sent TEXT NOT NULL DEFAULT '');
	CREATE INDEX changes_start ON changes(start_at);`,
	// Stand-ins of on-call rotations: who covers a rotation, and when.
	`CREATE TABLE oncall_overrides (id INTEGER PRIMARY KEY AUTOINCREMENT, rotation TEXT NOT NULL, member TEXT NOT NULL,
		from_at INTEGER NOT NULL, until_at INTEGER NOT NULL, by TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL);
	CREATE INDEX oncall_overrides_until ON oncall_overrides(until_at);`,
	// What a digest stands for and an escalation's own chain survive a
	// restart, so a delivered digest still credits its incidents.
	`ALTER TABLE intents ADD COLUMN covers TEXT NOT NULL DEFAULT '';
	ALTER TABLE intents ADD COLUMN chain TEXT NOT NULL DEFAULT '';`,
	// The audit log is a hash chain: every entry carries the hash of the
	// one before it, so an edited, inserted or removed entry shows up in
	// "unagikuma audit verify". Entries cannot be updated once chained.
	`ALTER TABLE audit ADD COLUMN risk TEXT NOT NULL DEFAULT '';
	ALTER TABLE audit ADD COLUMN prev TEXT NOT NULL DEFAULT '';
	ALTER TABLE audit ADD COLUMN hash TEXT NOT NULL DEFAULT '';
	CREATE INDEX audit_action ON audit(action);
	CREATE TRIGGER audit_append_only BEFORE UPDATE ON audit WHEN OLD.hash != ''
	BEGIN SELECT RAISE(ABORT, 'the audit log is append-only'); END;`,
	// The decision ledger: every step an alert went through, and why.
	`CREATE TABLE decisions (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, incident_id INTEGER NOT NULL DEFAULT 0,
		intent_id TEXT NOT NULL DEFAULT '', monitor TEXT NOT NULL DEFAULT '', step TEXT NOT NULL, data TEXT NOT NULL);
	CREATE INDEX decisions_incident ON decisions(incident_id);
	CREATE INDEX decisions_monitor_at ON decisions(monitor, at);
	CREATE INDEX decisions_at ON decisions(at);`,
	// Records merged from other sites of a cluster: tagged with the site
	// and the master term they were written under. Each site's audit log
	// keeps its own hash chain; marks make merging idempotent.
	`ALTER TABLE incidents ADD COLUMN site TEXT NOT NULL DEFAULT '';
	ALTER TABLE incidents ADD COLUMN term INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE events ADD COLUMN site TEXT NOT NULL DEFAULT '';
	ALTER TABLE decisions ADD COLUMN site TEXT NOT NULL DEFAULT '';
	ALTER TABLE deliveries ADD COLUMN site TEXT NOT NULL DEFAULT '';
	CREATE TABLE site_audit (site TEXT NOT NULL, term INTEGER NOT NULL DEFAULT 0, origin_id INTEGER NOT NULL, at INTEGER NOT NULL,
		actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL DEFAULT '', area TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '', addr TEXT NOT NULL DEFAULT '', risk TEXT NOT NULL DEFAULT '',
		prev TEXT NOT NULL DEFAULT '', hash TEXT NOT NULL DEFAULT '', PRIMARY KEY (site, origin_id));
	CREATE INDEX site_audit_at ON site_audit(at);
	CREATE TABLE merge_marks (site TEXT NOT NULL, tbl TEXT NOT NULL, last_id INTEGER NOT NULL, PRIMARY KEY (site, tbl));
	CREATE TABLE merged (site TEXT NOT NULL, tbl TEXT NOT NULL, origin_id INTEGER NOT NULL, local_id INTEGER NOT NULL,
		PRIMARY KEY (site, tbl, origin_id));`,
	// Configuration versions are scoped by the master term that wrote
	// them; a configuration a replaced master kept changing is kept aside,
	// shown, and never applied by itself.
	`ALTER TABLE config_snapshots ADD COLUMN term INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE diverged_configs (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, site TEXT NOT NULL,
		term INTEGER NOT NULL, serial INTEGER NOT NULL, sha256 TEXT NOT NULL, text TEXT NOT NULL, base_serial INTEGER NOT NULL DEFAULT 0,
		UNIQUE(site, sha256));`,
	// How the system reaches a person directly (a team manager, say): the
	// channels that deliver to them, by name.
	`ALTER TABLE people ADD COLUMN notify TEXT NOT NULL DEFAULT '';`,
	`CREATE TABLE site_observations(node TEXT NOT NULL,monitor TEXT NOT NULL,epoch TEXT NOT NULL,seq INTEGER NOT NULL,data TEXT NOT NULL,PRIMARY KEY(node,monitor));
 CREATE INDEX site_observations_delta ON site_observations(node,epoch,seq);
 CREATE TABLE site_observation_cursors(node TEXT PRIMARY KEY,epoch TEXT NOT NULL,seq INTEGER NOT NULL);`,
	`ALTER TABLE incidents ADD COLUMN source_node TEXT NOT NULL DEFAULT '';
 ALTER TABLE incidents ADD COLUMN source_epoch TEXT NOT NULL DEFAULT '';
 ALTER TABLE incidents ADD COLUMN source_id INTEGER NOT NULL DEFAULT 0;
 CREATE INDEX incidents_source ON incidents(source_node,source_epoch,source_id);
 ALTER TABLE events ADD COLUMN source_epoch TEXT NOT NULL DEFAULT '';
 ALTER TABLE decisions ADD COLUMN source_epoch TEXT NOT NULL DEFAULT '';
 ALTER TABLE deliveries ADD COLUMN source_epoch TEXT NOT NULL DEFAULT '';
 ALTER TABLE evidence ADD COLUMN site TEXT NOT NULL DEFAULT '';
 ALTER TABLE evidence ADD COLUMN source_epoch TEXT NOT NULL DEFAULT '';
 ALTER TABLE evidence ADD COLUMN original_id INTEGER NOT NULL DEFAULT 0;
 ALTER TABLE acks ADD COLUMN site TEXT NOT NULL DEFAULT '';
 ALTER TABLE acks ADD COLUMN source_epoch TEXT NOT NULL DEFAULT '';
 ALTER TABLE acks ADD COLUMN original_id INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE events ADD COLUMN original_id INTEGER NOT NULL DEFAULT 0;
 ALTER TABLE decisions ADD COLUMN original_id INTEGER NOT NULL DEFAULT 0;
 ALTER TABLE deliveries ADD COLUMN original_id INTEGER NOT NULL DEFAULT 0;
 CREATE INDEX events_source ON events(site,source_epoch,original_id);
 CREATE INDEX decisions_source ON decisions(site,source_epoch,original_id);
 CREATE INDEX deliveries_source ON deliveries(site,source_epoch,original_id);
 CREATE INDEX evidence_source ON evidence(site,source_epoch,original_id);
 CREATE INDEX acks_source ON acks(site,source_epoch,original_id);`,
	// Reserved for a later multi-tenant edition: always NULL for now.
	`ALTER TABLE users ADD COLUMN tenant_id TEXT;
 ALTER TABLE people ADD COLUMN tenant_id TEXT;
 ALTER TABLE monitors ADD COLUMN tenant_id TEXT;
 ALTER TABLE incidents ADD COLUMN tenant_id TEXT;`,
	// A contact on leave or transferred is not sent to ('' = active; left
	// is left_at).
	`ALTER TABLE people ADD COLUMN status TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE people ADD COLUMN no_report INTEGER NOT NULL DEFAULT 0;`,
	// Rejoin codes name the NODE they may reclaim after a master handover.
	`ALTER TABLE join_codes ADD COLUMN rejoin TEXT NOT NULL DEFAULT '';`,
}

var historyMigrations = []string{
	`CREATE TABLE verdicts (monitor TEXT NOT NULL, at INTEGER NOT NULL, verdict TEXT NOT NULL, status TEXT NOT NULL,
		hard INTEGER NOT NULL, reason TEXT, latency_ms REAL, excluded INTEGER NOT NULL DEFAULT 0, fail_nodes TEXT);
	CREATE INDEX verdicts_monitor_at ON verdicts(monitor, at);
	CREATE INDEX verdicts_at ON verdicts(at);
	CREATE TABLE agg1h (monitor TEXT NOT NULL, hour INTEGER NOT NULL, up_s REAL NOT NULL, down_s REAL NOT NULL,
		excluded_s REAL NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, lat_max REAL NOT NULL,
		PRIMARY KEY (monitor, hour));`,
	// Keyed by monitor id: rounds are a short ring for troubleshooting,
	// uptime and latency come from the 5-minute and hourly aggregates.
	// The rows of the first version are carried over by migrateHistoryV2.
	`CREATE TABLE rounds (mid INTEGER NOT NULL, at INTEGER NOT NULL, st INTEGER NOT NULL, flags INTEGER NOT NULL,
		lat REAL, reason TEXT, nodes TEXT);
	CREATE INDEX rounds_mid_at ON rounds(mid, at);
	CREATE INDEX rounds_at ON rounds(at);
	CREATE TABLE agg5m (mid INTEGER NOT NULL, t INTEGER NOT NULL, up_s REAL NOT NULL, down_s REAL NOT NULL, ex_s REAL NOT NULL,
		deg_s REAL NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, lat_max REAL NOT NULL, PRIMARY KEY (mid, t)) WITHOUT ROWID;
	CREATE TABLE hourly (mid INTEGER NOT NULL, t INTEGER NOT NULL, up_s REAL NOT NULL, down_s REAL NOT NULL, ex_s REAL NOT NULL,
		deg_s REAL NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, lat_max REAL NOT NULL, PRIMARY KEY (mid, t)) WITHOUT ROWID;`,
	`ALTER TABLE rounds ADD COLUMN site TEXT NOT NULL DEFAULT '';
	CREATE TABLE merge_marks (site TEXT NOT NULL, tbl TEXT NOT NULL, last_id INTEGER NOT NULL, PRIMARY KEY (site, tbl));`,
	`ALTER TABLE rounds ADD COLUMN source_epoch TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE rounds ADD COLUMN original_id INTEGER NOT NULL DEFAULT 0; CREATE INDEX rounds_source ON rounds(site,source_epoch,original_id);`,
	`ALTER TABLE rounds RENAME TO rounds_old_rowid;
 CREATE TABLE rounds (id INTEGER PRIMARY KEY AUTOINCREMENT, mid INTEGER NOT NULL, at INTEGER NOT NULL, st INTEGER NOT NULL, flags INTEGER NOT NULL,
 lat REAL, reason TEXT, nodes TEXT, site TEXT NOT NULL DEFAULT '', source_epoch TEXT NOT NULL DEFAULT '', original_id INTEGER NOT NULL DEFAULT 0);
 INSERT INTO rounds(id,mid,at,st,flags,lat,reason,nodes,site,source_epoch,original_id)
 SELECT rowid,mid,at,st,flags,lat,reason,nodes,site,source_epoch,original_id FROM rounds_old_rowid;
 DROP TABLE rounds_old_rowid;
 CREATE INDEX rounds_mid_at ON rounds(mid,at);
 CREATE INDEX rounds_at ON rounds(at);
 CREATE INDEX rounds_source ON rounds(site,source_epoch,original_id);`,
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
	if version > len(migrations) {
		return fmt.Errorf("%s.db was written by a newer unagikuma (schema %d > %d)", name, version, len(migrations))
	}
	if version < len(migrations) && version > 0 && name == "core" {
		// Keep a copy of the previous schema before upgrading it.
		backup := filepath.Join(s.dir, fmt.Sprintf("core.db.bak-%d", version))
		if _, err := os.Stat(backup); err != nil {
			if _, err := db.Exec(`VACUUM INTO ?`, backup); err != nil {
				return fmt.Errorf("backup before migration: %w", err)
			}
		}
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s migration %d: %w", name, i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
