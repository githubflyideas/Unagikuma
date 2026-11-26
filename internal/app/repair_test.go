package app

import (
	"os"
	"path/filepath"
	"strconv"
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

// While a first start is still in repair mode, an intervening banner
// must not rewrite history: a valid but over-limit fix attempt raises
// the monitor-limit banner, and a broken edit after it must go back to
// saying there is no earlier good version (invalid_first) — not claim
// a previous version keeps running when none has ever loaded.
func TestRepairBannerSurvivesMonitorLimitInterlude(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "monitors.conf")
	if err := os.WriteFile(conf, []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	if b := a.Banner(); b == nil || b.Code != "invalid_first" {
		t.Fatalf("banner = %+v, want invalid_first", b)
	}
	// A valid configuration past what a first start takes without a
	// licence (the import ceiling, 200): refused, repair mode continues
	// under the limit's banner.
	var sb strings.Builder
	sb.WriteString("@ IN SOA unagikuma. admin.localhost. ( 2026102302 1s 500ms 300ms 2 )\n")
	for i := 1; i <= 205; i++ {
		sb.WriteString("bulk" + strconv.Itoa(i) + " IN PING 192.0.2.1\n")
	}
	if err := os.WriteFile(conf, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	a.cfg.check()
	if b := a.Banner(); b == nil || b.Code != "monitor_limit" {
		t.Fatalf("banner after over-limit fix = %+v, want monitor_limit", b)
	}
	if n := len(a.Config().Config.Monitors); n != 0 {
		t.Fatalf("over-limit fix applied %d monitors, want 0", n)
	}
	// A broken edit now: still no earlier good version exists.
	broken2 := strings.Replace(brokenConfig, "2026102301", "2026102303", 1)
	if err := os.WriteFile(conf, []byte(broken2), 0o600); err != nil {
		t.Fatal(err)
	}
	a.cfg.check()
	if b := a.Banner(); b == nil || b.Code != "invalid_first" {
		t.Fatalf("banner after second broken edit = %+v, want invalid_first (no previous version has ever run)", b)
	}
}
