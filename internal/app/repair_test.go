package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenConfig has a record type that does not exist: validation fails
// with diagnostics, guaranteed.
const brokenConfig = `@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )
web     IN BOGUS http://192.168.1.10/
`

// A first start whose monitors.conf has errors and which has no earlier
// good version starts in repair mode: New succeeds, the running
// configuration is the empty starter (nothing is monitored), the banner
// carries the diagnostics, and the editor still shows the user's text.
// Saving a fixed configuration ends repair mode live.
func TestFirstStartInvalidConfigRepairMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatalf("first start with a broken config must start in repair mode: %v", err)
	}
	defer a.Store().Close()
	b := a.Banner()
	if b == nil || b.Code != "invalid_first" || len(b.Diags) == 0 {
		t.Fatalf("banner = %+v, want invalid_first with diagnostics", b)
	}
	if n := len(a.Config().Config.Monitors); n != 0 {
		t.Fatalf("repair mode runs %d monitors, want 0 (nothing probed)", n)
	}
	if a.Config().Text != brokenConfig {
		t.Fatalf("editor text = %q, want the user's own broken text", a.Config().Text)
	}
	// Fix it through the same save path the editor uses.
	st, err := a.SaveConfig([]byte(testConfig), a.Config().ETag(), "test", "fix")
	if err != nil {
		t.Fatalf("saving the fix: %v", err)
	}
	if len(st.Config.Monitors) == 0 {
		t.Fatal("the fixed configuration has no monitors")
	}
	if a.Banner() != nil {
		t.Fatalf("banner after the fix = %+v, want none (repair mode over)", a.Banner())
	}
}

// With an earlier good version on record, a broken file changes
// nothing: the last good configuration keeps running under the
// "invalid" banner — the repair path must not take over.
func TestInvalidConfigWithHistoryKeepsLastGood(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "monitors.conf")
	if err := os.WriteFile(conf, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatal(err)
	}
	a.Store().Close() // the good start left a snapshot behind

	if err := os.WriteFile(conf, []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	a2, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Store().Close()
	b := a2.Banner()
	if b == nil || b.Code != "invalid" {
		t.Fatalf("banner = %+v, want invalid (last good keeps running)", b)
	}
	if len(a2.Config().Config.Monitors) == 0 {
		t.Fatal("the last good configuration lost its monitors")
	}
	if !strings.Contains(a2.Config().Text, "IN HTTP") {
		t.Fatalf("running text = %q, want the last good version", a2.Config().Text)
	}
}
