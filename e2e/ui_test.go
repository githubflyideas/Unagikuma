//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestUI runs the browser tests in ui.mjs (Node and Playwright) against a
// fresh server. Without them it is skipped, unless UNAGIKUMA_E2E_UI=1.
func TestUI(t *testing.T) {
	need := os.Getenv("UNAGIKUMA_E2E_UI") == "1"
	if err := exec.Command("node", "-e", "require.resolve('playwright')").Run(); err != nil {
		if need {
			t.Fatalf("node with playwright is required: %v", err)
		}
		t.Skip("node with playwright not found")
	}
	s := start(t, "ui", fmt.Sprintf(soa, "ui")+`public IN STATUSPAGE "Public status" select="type:TCP"`+"\n"+
		// Failures are delivered, so the pages show channel statistics.
		"alerts IN CHANNEL log\ndefault IN POLICY chain=alerts\n")
	out := os.Getenv("UNAGIKUMA_E2E_OUT")
	if out == "" {
		out = t.TempDir()
	}
	cfg, _ := filepath.Abs("../internal/importer/testdata/nagios.cfg")
	cmd := exec.Command("node", "ui.mjs", s.url(""), out, cfg)
	b, err := cmd.CombinedOutput()
	t.Logf("\n%s", b)
	if err != nil {
		t.Fatalf("browser tests failed (screenshots in %s)", out)
	}
}
