package app

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// Shared maintenance propagates; another site's incident silence does not.
func TestReadCarry(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	until := now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	for _, q := range []string{
		`CREATE TABLE incidents (monitor TEXT, state TEXT, closed INTEGER, resolved_at INTEGER)`,
		`CREATE TABLE changes (id INTEGER, title TEXT, sel TEXT, start_at INTEGER, end_at INTEGER, state TEXT)`,
		`INSERT INTO incidents VALUES('flaky','{"silenced_until":"` + until + `"}',0,NULL)`,
		`INSERT INTO incidents VALUES('loud','{}',0,NULL)`,
		`INSERT INTO incidents VALUES('old','{"silenced_until":"` + until + `"}',1,5)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Exec(`INSERT INTO changes VALUES(7,'Upgrade','name:web',?,?,'planned')`, now.Add(-time.Minute).UnixMilli(), now.Add(time.Hour).UnixMilli())
	db.Exec(`INSERT INTO changes VALUES(8,'Live','name:web',?,?,'running')`, now.Add(-time.Minute).UnixMilli(), now.Add(time.Hour).UnixMilli())
	db.Exec(`INSERT INTO changes VALUES(9,'Done','name:web',?,?,'done')`, now.Add(-time.Minute).UnixMilli(), now.Add(time.Hour).UnixMilli())
	db.Close()
	c, err := readCarry(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Silences) != 0 || len(c.Changes) != 2 || c.Changes[0].Sel != "name:web" {
		t.Fatalf("%+v", c)
	}
}
