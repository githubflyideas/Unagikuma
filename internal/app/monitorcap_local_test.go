package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
)

func localZone(n int, prefix string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s%02d IN PING 10.20.0.%d\n", prefix, i, i)
	}
	return b.String()
}

// This site's own monitors count toward the monitor limit like the shared
// ones: a zone that would pass it is refused, and with the zone in place a
// shared monitor past the limit is refused too.
func TestSiteLocalCountsTowardLimit(t *testing.T) {
	a, _, _, dir := newApp(t, capConfig(95))
	defer a.Store().Close()
	if _, _, err := a.SetSiteLocal(localZone(5, "loc"), "test"); err != nil {
		t.Fatalf("five local within the limit: %v", err)
	}
	if got := a.MonitorCount(a.Config().Config); got != 100 {
		t.Fatalf("count with the zone: %d, want 100", got)
	}
	if _, diags, err := a.SetSiteLocal(localZone(6, "loc"), "test"); err == nil || len(diags) != 1 || diags[0].Code != "monitor_limit" {
		t.Fatalf("sixth local past the limit: %v %v", err, diags)
	}
	if _, err := a.EditConfig("test", "add", func(f *config.File) error {
		return f.UpsertRecord(config.RecordSpec{Name: "one-more", Type: "PING", Target: "10.9.9.9"})
	}); err == nil {
		t.Fatal("a shared monitor past the limit, counting the zone, was accepted")
	}
	// A local monitor named like a shared one runs as the shared one: not counted twice.
	if _, _, err := a.SetSiteLocal(localZone(5, "loc")+"m001 IN PING 10.30.0.1\n", "test"); err != nil {
		t.Fatalf("a shadowed name counted: %v", err)
	}
	// The zone's file changed by hand past the limit: the last applied zone runs on.
	if err := os.WriteFile(filepath.Join(dir, SiteLocalFile), []byte("$FILE revision=1\n"+localZone(9, "loc")), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Reapply()
	if got := a.MonitorCount(a.Config().Config); got != 100 {
		t.Fatalf("count after a hand edit past the limit: %d, want 100", got)
	}
	if b := a.Banner(); b == nil || b.Code != "monitor_limit" {
		t.Fatalf("banner: %+v", b)
	}
}

// The limit holds at startup: a file grown past it while the program was
// stopped is not applied, the last version runs on.
func TestStartupHoldsMonitorLimit(t *testing.T) {
	a, _, _, dir := newApp(t, capConfig(100))
	a.Store().Close()
	grown := strings.Replace(capConfig(110), "2026102301", "2026102302", 1)
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(grown), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Store().Close()
	if got := len(b.Config().Config.Monitors); got != 100 {
		t.Fatalf("monitors after a restart past the limit: %d, want the last 100", got)
	}
	if bn := b.Banner(); bn == nil || bn.Code != "monitor_limit" {
		t.Fatalf("banner: %+v", bn)
	}
}

// A first start takes the file like an import: up to 200 it runs, past
// that nothing is monitored until it is cut down or registered.
func TestFirstStartTakesUpToTheImportCeiling(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(150))
	if got := len(a.Config().Config.Monitors); got != 150 {
		t.Fatalf("150 on a first start: %d", got)
	}
	a.Store().Close()
	b, _, _, dir := newApp(t, capConfig(250))
	defer b.Store().Close()
	if got := len(b.Config().Config.Monitors); got != 0 {
		t.Fatalf("250 on a first start ran %d monitors", got)
	}
	if bn := b.Banner(); bn == nil || bn.Code != "monitor_limit_first" {
		t.Fatalf("banner: %+v", bn)
	}
	// Cut down to 200 with a higher revision: monitoring starts.
	cut := strings.Replace(capConfig(200), "2026102301", "2026102302", 1)
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(cut), 0o600); err != nil {
		t.Fatal(err)
	}
	b.cfg.check()
	if got := len(b.Config().Config.Monitors); got != 200 {
		t.Fatalf("after cutting to 200: %d", got)
	}
}

func gatusFile(n int) []byte {
	var b strings.Builder
	b.WriteString("endpoints:\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "  - name: imp%03d\n    url: http://10.40.%d.%d/\n    interval: 60s\n    conditions:\n      - \"[STATUS] == 200\"\n", i, i/250, i%250+1)
	}
	return []byte(b.String())
}

// An import can go to this site's own zone: the shared file is untouched,
// the monitors run here, and the import ceiling (200), not the everyday
// limit, holds the count.
func TestImportIntoSiteLocal(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(95))
	defer a.Store().Close()
	before := a.Config().Text
	if _, res, _, err := a.ImportKuma(gatusFile(20), "test", KumaOptions{Source: "gatus", Local: true}); err != nil || res.Monitors != 20 {
		t.Fatalf("import into the site's zone: %v %+v", err, res)
	}
	if a.Config().Text != before {
		t.Fatal("the shared configuration changed")
	}
	if !strings.Contains(a.SiteLocal(), "imp020") {
		t.Fatalf("zone: %q", a.SiteLocal())
	}
	if got := a.MonitorCount(a.Config().Config); got != 115 {
		t.Fatalf("count after importing 20 locally: %d, want 115 (past 100, within the ceiling)", got)
	}
}

// On a node of a cluster a shared import is refused at once, before
// anything is stored; one into the site's own zone works.
func TestImportOnNodeIsLocalOnly(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(5))
	defer a.Store().Close()
	a.am.member = &Membership{Node: "b", Server: "https://master.invalid"}
	if _, _, _, err := a.ImportKuma(gatusFile(3), "test", KumaOptions{Source: "gatus"}); !errors.Is(err, ErrMember) {
		t.Fatalf("shared import on a node: %v", err)
	}
	if _, res, _, err := a.ImportKuma(gatusFile(3), "test", KumaOptions{Source: "gatus", Local: true}); err != nil || res.Monitors != 3 {
		t.Fatalf("local import on a node: %v", err)
	}
}

// The master keeps what a node says about its own configuration: a copy of
// its text (never applied) and its monitors, counted toward the limit for
// the nodes the configuration still has.
func TestMasterKeepsSiteLocalReports(t *testing.T) {
	a, _, _, dir := newApp(t, capConfig(50)+"b IN NODE Beijing\n")
	defer a.Store().Close()
	text := localZone(30, "bj")
	a.onSiteLocalReport("b", 30, "h1", &text)
	if b, err := os.ReadFile(filepath.Join(dir, "sites", "b."+SiteLocalFile)); err != nil || string(b) != text {
		t.Fatalf("copy: %v %q", err, b)
	}
	if got := a.MonitorCount(a.Config().Config); got != 80 {
		t.Fatalf("count with b's own monitors: %d, want 80", got)
	}
	// A node the configuration no longer has is not counted.
	a.onSiteLocalReport("gone", 40, "h2", nil)
	if got := a.MonitorCount(a.Config().Config); got != 80 {
		t.Fatalf("count with a removed node: %d", got)
	}
	// The shared configuration can now grow only to 100 with b's 30.
	if _, err := a.EditConfig("test", "add", func(f *config.File) error {
		for i := 0; i < 21; i++ {
			if err := f.UpsertRecord(config.RecordSpec{Name: fmt.Sprintf("x%02d", i), Type: "PING", Target: "10.9.9.9"}); err != nil {
				return err
			}
		}
		return nil
	}); err == nil {
		t.Fatal("101 with b's own monitors accepted")
	}
}

// A node judges additions to its own configuration by the master's word on
// the cluster's standing; with no limit there, nothing is refused.
func TestNodeJudgesLocalByClusterUsage(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(5))
	defer a.Store().Close()
	a.am.member = &Membership{Node: "b", Server: "https://master.invalid"}
	a.setClusterUsage(cluster.Usage{Used: 98, Limit: 100})
	if _, diags, err := a.SetSiteLocal(localZone(3, "loc"), "test"); err == nil || len(diags) != 1 || diags[0].Code != "monitor_limit" {
		t.Fatalf("101 in the cluster accepted: %v", err)
	}
	if _, _, err := a.SetSiteLocal(localZone(2, "loc"), "test"); err != nil {
		t.Fatalf("100 in the cluster refused: %v", err)
	}
	if c := a.MonitorCap(); c.Limit != 100 || c.Used != 98 {
		t.Fatalf("the node shows its own standing, not the cluster's: %+v", c)
	}
	a.setClusterUsage(cluster.Usage{Used: 500, Limit: 0})
	if _, _, err := a.SetSiteLocal(localZone(50, "loc"), "test"); err != nil {
		t.Fatalf("a registered cluster refused: %v", err)
	}
}
