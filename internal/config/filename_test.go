package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathInPrefersNewName(t *testing.T) {
	dir := t.TempDir()
	if got := PathIn(dir); got != filepath.Join(dir, FileName) {
		t.Fatalf("empty dir: %s", got)
	}
	old := filepath.Join(dir, LegacyFileName)
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := PathIn(dir); got != old {
		t.Fatalf("legacy only: %s", got)
	}
	moved, err := MigrateFileName(old, filepath.Join(dir, FileName))
	if err != nil || !moved {
		t.Fatalf("migrate: %v %v", moved, err)
	}
	if got := PathIn(dir); got != filepath.Join(dir, FileName) {
		t.Fatalf("after rename: %s", got)
	}
	if moved, _ := MigrateFileName(old, filepath.Join(dir, FileName)); moved {
		t.Fatal("renamed twice")
	}
}
