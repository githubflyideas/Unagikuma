package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// wantCoreCols is the core.db schema of a fresh 3.0.1 database: the
// previous migration chain's final columns, without tenant_id or the
// unused on-call table.
var wantCoreCols = map[string][]string{
	"acks":                     {"id", "incident_id", "at", "by", "via", "action", "comment", "site", "source_epoch", "original_id"},
	"agent_tokens":             {"node", "prefix", "hash", "created_at", "last_used", "last_addr"},
	"announcements":            {"id", "page", "title", "body", "severity", "author", "created_at", "updated_at", "resolved_at"},
	"audit":                    {"id", "at", "actor", "action", "target", "area", "detail", "addr", "risk", "prev", "hash"},
	"bounces":                  {"kind", "value", "n", "first_at", "last_at", "reason"},
	"changes":                  {"id", "title", "description", "impact", "sel", "policy", "channels", "status_page", "announcement_id", "start_at", "end_at", "state", "note", "created_by", "created_at", "updated_at", "finished_at", "finished_by", "sent"},
	"config_snapshots":         {"serial", "at", "source", "author", "message", "text", "sha256", "summary", "term"},
	"contacts":                 {"id", "person_id", "kind", "value"},
	"counters":                 {"day", "name", "n"},
	"decisions":                {"id", "at", "incident_id", "intent_id", "monitor", "step", "data", "site", "source_epoch", "original_id"},
	"deliveries":               {"id", "intent_id", "incident_id", "kind", "seq", "at", "monitor", "title", "attempts", "ok", "fallback", "drill", "delayed_s", "site", "source_epoch", "original_id"},
	"diverged_configs":         {"id", "at", "site", "term", "serial", "sha256", "text", "base_serial"},
	"events":                   {"id", "at", "type", "severity", "monitor", "message", "data", "site", "source_epoch", "original_id"},
	"evidence":                 {"id", "monitor", "node", "at", "result", "site", "source_epoch", "original_id"},
	"incidents":                {"id", "monitor", "kind", "system", "policy", "reason", "detail", "title", "ack_comment", "summary", "state", "opened_at", "resolved_at", "closed", "acked", "drill", "updated_at", "site", "term", "source_node", "source_epoch", "source_id"},
	"intents":                  {"id", "incident_id", "seq", "kind", "policy", "message", "delivery", "state", "created_at", "due_at", "updated_at", "system", "force_fail", "attempts", "covers", "chain"},
	"join_codes":               {"hash", "created_at", "expires_at", "used_at", "node", "rejoin"},
	"merge_marks":              {"site", "tbl", "last_id"},
	"merged":                   {"site", "tbl", "origin_id", "local_id"},
	"monitor_state":            {"name", "state", "status", "reason", "detail", "latency_ms", "last_check", "last_change", "incident_id", "def_hash"},
	"monitors":                 {"id", "name", "def_hash", "created_at", "retired_at"},
	"people":                   {"id", "handle", "name", "note", "username", "left_at", "created_at", "updated_at", "notify", "status", "no_report"},
	"push_tokens":              {"hash", "monitor", "created_at"},
	"saved_alerts":             {"at", "name", "monitor", "n"},
	"secrets":                  {"name", "key_id", "dek", "value", "scopes", "updated_at"},
	"sessions":                 {"id_hash", "user_id", "csrf", "created_at", "expires_at"},
	"settings":                 {"key", "value"},
	"site_audit":               {"site", "term", "origin_id", "at", "actor", "action", "target", "area", "detail", "addr", "risk", "prev", "hash"},
	"site_observation_cursors": {"node", "epoch", "seq"},
	"site_observations":        {"node", "monitor", "epoch", "seq", "data"},
	"subscribers":              {"id", "page", "email", "token", "created_at", "confirmed_at"},
	"tokens":                   {"id", "name", "prefix", "hash", "scopes", "user_id", "created_at", "last_used"},
	"users":                    {"id", "username", "password", "created_at", "prefs", "role", "areas", "disabled", "groups"},
	"value_samples":            {"monitor", "at", "v", "def_hash"},
}

var wantHistoryCols = map[string][]string{
	"agg5m":       {"mid", "t", "up_s", "down_s", "ex_s", "deg_s", "lat_sum", "lat_n", "lat_max"},
	"hourly":      {"mid", "t", "up_s", "down_s", "ex_s", "deg_s", "lat_sum", "lat_n", "lat_max"},
	"merge_marks": {"site", "tbl", "last_id"},
	"rounds":      {"id", "mid", "at", "st", "flags", "lat", "reason", "nodes", "site", "source_epoch", "original_id"},
}

func TestFreshSchema(t *testing.T) {
	s, _, _ := open(t)
	assertVersion(t, s.core, schemaEpoch)
	assertVersion(t, s.hist, schemaEpoch)
	assertCols(t, s.core, "core", wantCoreCols)
	assertCols(t, s.hist, "history", wantHistoryCols)
	if !tablesHave(t, s.core, "audit_append_only") {
		t.Error("missing audit_append_only trigger")
	}
	for _, name := range []string{"tenant_id", "oncall_overrides", "verdicts", "agg1h"} {
		if tablesHave(t, s.core, name) || tablesHave(t, s.hist, name) || columnExists(t, s.core, name) || columnExists(t, s.hist, name) {
			t.Errorf("removed name %s is still in the schema", name)
		}
	}
	// Reopening a current database is a no-op.
	dir := t.TempDir()
	s1, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestOldSchemaRejected(t *testing.T) {
	for _, tt := range []struct {
		file    string
		version int
	}{
		{"core.db", 29},
		{"core.db", 1},
		{"history.db", 6},
		{"history.db", 1},
	} {
		dir := t.TempDir()
		writeVersionedDB(t, filepath.Join(dir, tt.file), tt.version)
		_, err := Open(dir, Options{})
		if err == nil {
			t.Fatalf("%s schema %d: opened", tt.file, tt.version)
		}
		msg := err.Error()
		if !strings.Contains(msg, "before 3.0.1") || !strings.Contains(msg, "fresh data directory") {
			t.Errorf("%s schema %d: %v", tt.file, tt.version, err)
		}
		if strings.Contains(msg, "newer unagikuma") {
			t.Errorf("%s schema %d reported as newer: %v", tt.file, tt.version, err)
		}
	}
}

func TestNewerSchemaRejected(t *testing.T) {
	dir := t.TempDir()
	writeVersionedDB(t, filepath.Join(dir, "core.db"), schemaEpoch+50)
	_, err := Open(dir, Options{})
	if err == nil || !strings.Contains(err.Error(), "newer unagikuma") {
		t.Fatalf("got %v", err)
	}
}

func assertVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("user_version %d, want %d", got, want)
	}
}

func assertCols(t *testing.T, db *sql.DB, label string, want map[string][]string) {
	t.Helper()
	got := map[string][]string{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		cols, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			got[table] = append(got[table], name)
		}
		cols.Close()
	}
	for table, cols := range want {
		if strings.Join(got[table], ",") != strings.Join(cols, ",") {
			t.Errorf("%s %s columns:\n  got  %v\n  want %v", label, table, got[table], cols)
		}
	}
	for table := range got {
		if _, ok := want[table]; !ok {
			t.Errorf("%s unexpected table %s", label, table)
		}
	}
	for table := range want {
		if _, ok := got[table]; !ok {
			t.Errorf("%s missing table %s", label, table)
		}
	}
}

func tablesHave(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func columnExists(t *testing.T, db *sql.DB, col string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	for _, table := range tables {
		info, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for info.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := info.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				info.Close()
				t.Fatal(err)
			}
			if name == col {
				info.Close()
				return true
			}
		}
		info.Close()
	}
	return false
}

func writeVersionedDB(t *testing.T, path string, version int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE placeholder (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
}
