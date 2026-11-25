package cluster

import (
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// A node only gets the checks it can run; one that announces nothing is
// an older release with the original check types.
func TestConfigFollowsNodeCapabilities(t *testing.T) {
	cfg := &AgentConfig{Hash: "h", Monitors: []AgentMonitor{
		{Name: "web", Probe: probe.Spec{Type: "HTTP"}},
		{Name: "exp", Probe: probe.Spec{Type: "DOMAIN"}},
	}}
	for _, c := range []struct {
		caps []string
		keep []string
	}{
		{nil, []string{"web"}},
		{[]string{"check:HTTP", "check:DOMAIN"}, []string{"web", "exp"}},
	} {
		p := &peer{caps: capSet(c.caps), cfgReady: make(chan struct{}, 1), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		p.setConfig(cfg)
		var got []string
		for _, m := range p.pending.Monitors {
			got = append(got, m.Name)
		}
		if !slices.Equal(got, c.keep) {
			t.Errorf("caps %v: sent %v, want %v", c.caps, got, c.keep)
		}
		if want := len(cfg.Monitors) - len(c.keep); len(p.status.Unsupported) != want {
			t.Errorf("caps %v: unsupported %v", c.caps, p.status.Unsupported)
		}
	}
	if len(cfg.Monitors) != 2 {
		t.Fatal("filtering changed the shared config")
	}
	for _, c := range LocalCaps() {
		if !capSet(LocalCaps())[c] {
			t.Fatal(c)
		}
	}
}
