package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func setVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatal(err)
	}
}

// A database from a newer version, or from before 3.0.1, is refused before
// a restore is staged; a current one and an empty folder pass.
func TestCheckSchema(t *testing.T) {
	dir := t.TempDir()
	if err := CheckSchema(dir); err != nil {
		t.Fatalf("empty folder: %v", err)
	}
	st, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := CheckSchema(dir); err != nil {
		t.Fatalf("current databases: %v", err)
	}
	core := filepath.Join(dir, "core.db")
	setVersion(t, core, schemaEpoch+len(coreMigrations)+5)
	if err := CheckSchema(dir); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("newer database: %v", err)
	}
	setVersion(t, core, 12)
	if err := CheckSchema(dir); err == nil || !strings.Contains(err.Error(), "before 3.0.1") {
		t.Fatalf("older database: %v", err)
	}
}
