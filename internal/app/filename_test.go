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
	if _, err := os.Stat(filepath.Join(dir, SiteLocalFile)); err != nil {
		t.Errorf("site-local file not renamed to %s: %v", SiteLocalFile, err)
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

// The 2.6 name of the site-local file is renamed too.
func TestSiteLocalConfRenamed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "site-local.conf"), []byte("$FILE revision=1\nr IN PING 192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProbes{fail: map[string]bool{}}
	a, err := New(Options{DataDir: dir, Probe: fp.run, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() })
	if a.SiteLocal() == "" {
		t.Fatal("site-local configuration lost in the rename")
	}
}

// A zone typed without a header gets one.
func TestSiteLocalHeaderAdded(t *testing.T) {
	for in, want := range map[string]string{
		"r IN PING 192.0.2.1\n":                   "$FILE revision=1\nr IN PING 192.0.2.1\n",
		"; mine\n$FILE revision=7\nr IN PING x\n": "; mine\n$FILE revision=7\nr IN PING x\n",
		"$file revision=2\n":                      "$file revision=2\n",
	} {
		if got := withFileHeader(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}
