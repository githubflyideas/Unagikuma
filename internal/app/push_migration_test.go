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
