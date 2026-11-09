package app

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// A data directory from before 2.6 has monitors.bind (and site-local.bind,
// replica/master.bind); the server renames them and keeps running the same
// configuration.
func TestLegacyFileNamesAreRenamed(t *testing.T) {
	dir := t.TempDir()
	text := "$FILE revision=2026110901\nweb IN HTTP https://example.com/\n"
	if err := os.WriteFile(filepath.Join(dir, "monitors.bind"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site-local.bind"), []byte("$FILE revision=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProbes{fail: map[string]bool{}}
	a, err := New(Options{DataDir: dir, Probe: fp.run, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() }) // Windows cannot remove an open core.db
	if a.ConfigPath() != filepath.Join(dir, "monitors.conf") {
		t.Fatalf("config path %s", a.ConfigPath())
	}
	for _, n := range []string{"monitors.bind", "site-local.bind"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", n, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "monitors.conf")); string(b) != text {
		t.Fatalf("renamed file differs: %q", b)
	}
	if a.Config().Config.FindMonitor("web") == nil {
		t.Fatal("monitor of the renamed file not loaded")
	}
}
