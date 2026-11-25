package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyPushReceiverMigration(t *testing.T) {
	a, _, _, dir := newApp(t, testConfig+"b IN NODE other\nlegacy-push IN PUSH period=1h nodes=unagikuma,b\n")
	defer a.Store().Close()
	m := a.Config().Config.FindMonitor("legacy-push")
	if len(m.Nodes) != 1 || m.Nodes[0] != "unagikuma" {
		t.Fatal(m.Nodes)
	}
	old, err := os.ReadFile(filepath.Join(dir, "monitors.conf.before-per-site"))
	if err != nil || !strings.Contains(string(old), "nodes=unagikuma,b") {
		t.Fatal("missing original config", err)
	}
}

// A PUSH monitor written for an older release with check options it
// ignored still starts: the options are removed once, the original kept.
func TestLegacyPushOptionsMigration(t *testing.T) {
	a, _, _, dir := newApp(t, testConfig+"old-push IN PUSH period=1h warn=5 crit=10\n")
	defer a.Store().Close()
	m := a.Config().Config.FindMonitor("old-push")
	if m == nil || m.Opts["warn"] != "" || m.Opts["crit"] != "" || m.Opts["period"] != "1h" {
		t.Fatal(m)
	}
	old, err := os.ReadFile(filepath.Join(dir, "monitors.conf.before-push-options"))
	if err != nil || !strings.Contains(string(old), "warn=5 crit=10") {
		t.Fatal("missing original config", err)
	}
}
