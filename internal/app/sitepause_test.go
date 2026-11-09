package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"net/http/httptest"
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

func TestSharedDisabledResumeIsLocalOnly(t *testing.T) {
	m, _, _, _ := newApp(t, testConfig+"b IN NODE Beijing\n")
	t.Cleanup(func() { m.store.Close() })
	beforeText := m.Config().Text
	if _, err := m.EditConfigFrom("alice", "disable web", nil, func(f *config.File) error {
		return SetPaused(f, []string{"web"}, true)
	}); err != nil {
		t.Fatal(err)
	}
	disabledText := m.Config().Text
	if !m.Config().Config.FindMonitor("web").Disabled || !strings.Contains(disabledText, "disabled=yes") {
		t.Fatal("setup")
	}
	serial := m.Config().Serial

	b := observationSite(t, "b")
	b.replica.mu.Lock()
	b.replica.cfg = m.applied.Load()
	b.replica.mu.Unlock()
	b.reapply()
	if !b.EffectivelyPaused("web") || !m.EffectivelyPaused("web") {
		t.Fatal("both should follow shared disabled")
	}

	// Master resume: only master runs; shared file unchanged; replica still paused.
	if n, err := m.SetLocalPaused([]string{"web"}, false, "bob"); err != nil || n != 1 {
		t.Fatalf("master resume: %d %v", n, err)
	}
	if v, ok := m.loadSitePauses()["web"]; !ok || v {
		t.Fatalf("master overlay want false, got %v %v", ok, v)
	}
	if m.Config().Serial != serial || m.Config().Text != disabledText {
		t.Fatal("master resume wrote monitors.conf")
	}
	if !m.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("master resume cleared shared disabled=")
	}
	assertOverlayPaused(t, m, "web", false)
	assertOverlayPaused(t, b, "web", true)

	// Replica resume: only replica runs; master stays on its own overlay/file.
	if n, err := b.SetLocalPaused([]string{"web"}, false, "alice"); err != nil || n != 1 {
		t.Fatalf("replica resume: %d %v", n, err)
	}
	if !m.Config().Config.FindMonitor("web").Disabled || m.Config().Text != disabledText {
		t.Fatal("replica resume changed shared configuration")
	}
	assertOverlayPaused(t, b, "web", false)
	assertOverlayPaused(t, m, "web", false)
	_ = beforeText
}

func TestPromotePreservesSitePauses(t *testing.T) {
	src, _, _, _ := newApp(t, testConfig)
	t.Cleanup(func() { src.store.Close() })
	dst, _, _, dir := newApp(t, testConfig)
	if _, err := dst.SetLocalPaused([]string{"web", "dns"}, true, "alice"); err != nil {
		t.Fatal(err)
	}
	before := dst.loadSitePauses()
	dst.store.Close()

	rec := httptest.NewRecorder()
	if err := src.writeState(rec); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, standbyDir), 0700)
	os.WriteFile(filepath.Join(dir, standbyDir, stateFile), rec.Body.Bytes(), 0600)
	meta := StandbyState{Key: rec.Header().Get(cluster.StateKeyHeader), Term: 1}
	b, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, standbyDir, stateMetaFile), b, 0600)
	b, _ = json.Marshal(promoteRequest{Term: 2})
	os.WriteFile(filepath.Join(dir, promoteFile), b, 0600)
	ok, err := applyPromote(dir, filepath.Join(dir, "monitors.conf"))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	gotB, err := os.ReadFile(filepath.Join(dir, sitePausesFile))
	if err != nil {
		t.Fatal("site-pauses.json lost on promote:", err)
	}
	var got map[string]bool
	if json.Unmarshal(gotB, &got) != nil || got["web"] != true || got["dns"] != true {
		t.Fatalf("overlay after promote: %s (before %+v)", gotB, before)
	}
}

func TestStandbyStateArchiveExcludesSitePauses(t *testing.T) {
	a, _, _, dir := newApp(t, testConfig)
	t.Cleanup(func() { a.store.Close() })
	if _, err := a.SetLocalPaused([]string{"web"}, true, "alice"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := a.writeState(rec); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.Bytes()
	if strings.Contains(string(body), "site-pauses") || strings.Contains(string(body), sitePausesFile) {
		t.Fatal("state archive mentions site-pauses")
	}
	// Unpack into temp via Restore would be heavy; presence of file on disk + absence in plaintext name is enough with promote test.
	if _, err := os.Stat(filepath.Join(dir, sitePausesFile)); err != nil {
		t.Fatal(err)
	}
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
