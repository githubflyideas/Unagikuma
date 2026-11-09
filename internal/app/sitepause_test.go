package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
)

func TestSiteLocalPauseIndependentOfMaster(t *testing.T) {
	m, _, _, _ := newApp(t, testConfig+"b IN NODE Beijing\nc IN NODE Tokyo\n")
	t.Cleanup(func() { m.store.Close() })
	b := observationSite(t, "b")
	if !b.IsMember() || m.IsMember() {
		t.Fatal("membership")
	}
	if _, err := b.EditConfig("alice", "x", nil); !errors.Is(err, ErrMember) {
		t.Fatalf("node wrote configuration: %v", err)
	}
	if n, err := b.SetLocalPaused([]string{"web"}, true, "alice"); err != nil || n != 1 {
		t.Fatalf("pause on node: %d %v", n, err)
	}
	if !b.LocalPaused("web") || !b.EffectivelyPaused("web") {
		t.Fatal("node overlay missing")
	}
	if mon := m.Config().Config.FindMonitor("web"); mon == nil || mon.Disabled {
		t.Fatal("master configuration changed")
	}
	if mon := b.Config().Config.FindMonitor("web"); mon != nil && mon.Disabled {
		t.Fatal("node wrote disabled=yes")
	}
	assertOverlayPaused(t, b, "web", true)
	assertOverlayPaused(t, m, "web", false)

	if n, err := m.SetLocalPaused([]string{"web"}, true, "bob"); err != nil || n != 1 {
		t.Fatalf("pause on master: %d %v", n, err)
	}
	assertOverlayPaused(t, m, "web", true)
	if m.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("master pause wrote disabled=yes")
	}

	if n, err := b.SetLocalPaused([]string{"web"}, false, "alice"); err != nil || n != 1 {
		t.Fatalf("resume on node: %d %v", n, err)
	}
	assertOverlayPaused(t, b, "web", false)
	assertOverlayPaused(t, m, "web", true)

	if _, err := b.SetLocalPaused([]string{"nope"}, true, "alice"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	b.reapply()
	assertOverlayPaused(t, b, "web", false)
	if n, err := b.SetLocalPaused([]string{"web"}, true, "alice"); err != nil || n != 1 {
		t.Fatal(err)
	}
	assertOverlayPaused(t, b, "web", true)
	b.reapply()
	assertOverlayPaused(t, b, "web", true)

	if _, err := os.Stat(filepath.Join(b.opts.DataDir, sitePausesFile)); err != nil {
		t.Fatal(err)
	}
}

func TestSiteLocalPausePersistsAcrossReopen(t *testing.T) {
	a, fp, _, dir := newApp(t, testConfig)
	t.Cleanup(func() { a.store.Close() })
	if _, err := a.SetLocalPaused([]string{"web", "dns"}, true, "alice"); err != nil {
		t.Fatal(err)
	}
	a.store.Close()

	a2, err := New(Options{DataDir: dir, Probe: fp.run, Log: a.log, Now: a.opts.Now, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a2.store.Close() })
	if !a2.LocalPaused("web") || !a2.LocalPaused("dns") || a2.LocalPaused("backup") {
		t.Fatalf("reopened overlay: web=%v dns=%v backup=%v", a2.LocalPaused("web"), a2.LocalPaused("dns"), a2.LocalPaused("backup"))
	}
	assertOverlayPaused(t, a2, "web", true)
	assertOverlayPaused(t, a2, "dns", true)
	assertOverlayPaused(t, a2, "backup", false)
}

func TestReplicaResumeOverridesSharedDisabled(t *testing.T) {
	m, _, _, _ := newApp(t, testConfig+"b IN NODE Beijing\n")
	t.Cleanup(func() { m.store.Close() })
	if _, err := m.EditConfigFrom("alice", "disable web", nil, func(f *config.File) error {
		return SetPaused(f, []string{"web"}, true)
	}); err != nil {
		t.Fatal(err)
	}
	if !m.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("setup")
	}
	b := observationSite(t, "b")
	b.replica.mu.Lock()
	b.replica.cfg = m.applied.Load()
	b.replica.mu.Unlock()
	b.reapply()
	if !b.EffectivelyPaused("web") {
		t.Fatal("replica should see shared disabled")
	}
	if n, err := b.SetLocalPaused([]string{"web"}, false, "alice"); err != nil || n != 1 {
		t.Fatalf("resume on replica: %d %v", n, err)
	}
	if !m.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("replica resume cleared master's disabled=")
	}
	if v, ok := b.loadSitePauses()["web"]; !ok || v {
		t.Fatalf("expected force-run overlay, got %v %v", ok, v)
	}
	assertOverlayPaused(t, b, "web", false)
	assertOverlayPaused(t, m, "web", true) // master still follows disabled=
}

// assertOverlayPaused checks applySitePauses against a one-monitor engine config.
func assertOverlayPaused(t *testing.T, a *App, name string, want bool) {
	t.Helper()
	disabled := false
	if cfg := a.EffectiveConfig(); cfg != nil {
		if m := cfg.FindMonitor(name); m != nil {
			disabled = m.Disabled
		}
	}
	ec := engine.Config{Monitors: []engine.MonitorSpec{{Name: name, Paused: disabled}}}
	a.applySitePauses(&ec)
	if ec.Monitors[0].Paused != want {
		t.Fatalf("%s: overlay+disabled → paused=%v want %v (overlay=%v disabled=%v)", name, ec.Monitors[0].Paused, want, a.loadSitePauses(), disabled)
	}
	if a.EffectivelyPaused(name) != want {
		t.Fatalf("%s: EffectivelyPaused=%v want %v", name, a.EffectivelyPaused(name), want)
	}
}
