package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
)

func localZone(n int, prefix string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s%02d IN PING 10.20.0.%d\n", prefix, i, i)
	}
	return b.String()
}

// In a single installation this site's own monitors count toward the
// limit like the shared ones: a zone that would pass it is refused, and
// with the zone in place a shared monitor past the limit is refused too.
func TestSiteLocalCountsTowardLimit(t *testing.T) {
	a, _, _, dir := newApp(t, capConfig(295))
	defer a.Store().Close()
	if _, _, err := a.SetSiteLocal(localZone(5, "loc"), "test"); err != nil {
		t.Fatalf("five local within the limit: %v", err)
	}
	if got := a.MonitorCount(a.Config().Config); got != 300 {
		t.Fatalf("count with the zone: %d, want 300", got)
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
	if got := a.MonitorCount(a.Config().Config); got != 300 {
		t.Fatalf("count after a hand edit past the limit: %d, want 300", got)
	}
	if b := a.Banner(); b == nil || b.Code != "monitor_limit" {
		t.Fatalf("banner: %+v", b)
	}
}

// In a cluster the shared configuration has its limit and each site's own
// zone its own, judged apart.
func TestClusterLimitsApart(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(100)+"b IN NODE Beijing\n")
	defer a.Store().Close()
	if _, err := a.EditConfig("test", "add", func(f *config.File) error {
		return f.UpsertRecord(config.RecordSpec{Name: "one-more", Type: "PING", Target: "10.9.9.9"})
	}); err == nil {
		t.Fatal("a 101st shared monitor was accepted")
	}
	if _, _, err := a.SetSiteLocal(localZone(99, "loc"), "test"); err != nil {
		t.Fatalf("99 of the site's own next to 100 shared: %v", err)
	}
	if _, diags, err := a.SetSiteLocal(localZone(99, "loc")+"loc-x IN PING 10.30.0.1\nloc-y IN PING 10.30.0.2\n", "test"); err == nil || len(diags) != 1 || diags[0].Code != "monitor_limit" {
		t.Fatalf("101 of the site's own accepted: %v", err)
	}
	if c := a.MonitorCap(); c.Used != 100 || c.Limit != 100 || c.SiteUsed != 99 || c.SiteLimit != 100 {
		t.Fatalf("standing: %+v", c)
	}
}

// The limit holds at startup: a file grown past it while the program was
// stopped is not applied, the last version runs on.
func TestStartupHoldsMonitorLimit(t *testing.T) {
	a, _, _, dir := newApp(t, capConfig(300))
	a.Store().Close()
	grown := strings.Replace(capConfig(310), "2026102301", "2026102302", 1)
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(grown), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Store().Close()
	if got := len(b.Config().Config.Monitors); got != 300 {
		t.Fatalf("monitors after a restart past the limit: %d, want the last 300", got)
	}
	if bn := b.Banner(); bn == nil || bn.Code != "monitor_limit" {
		t.Fatalf("banner: %+v", bn)
	}
}

// A first start has nothing to compare with: the file runs as it is, past
// the limit too, and the standing says it is over.
func TestFirstStartRunsEverything(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(350))
	defer a.Store().Close()
	if got := len(a.Config().Config.Monitors); got != 350 {
		t.Fatalf("350 on a first start ran %d", got)
	}
	if c := a.MonitorCap(); !c.Over || c.Used != 350 {
		t.Fatalf("standing: %+v", c)
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
// the monitors run here, and the monitor limit holds the count.
func TestImportIntoSiteLocal(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(285))
	defer a.Store().Close()
	before := a.Config().Text
	if _, res, _, err := a.ImportKuma(gatusFile(20), "test", KumaOptions{Source: "gatus", Local: true}); err != nil || res.Monitors != 15 {
		t.Fatalf("import into the site's zone: %v %+v", err, res)
	}
	if a.Config().Text != before {
		t.Fatal("the shared configuration changed")
	}
	if !strings.Contains(a.SiteLocal(), "imp015") || strings.Contains(a.SiteLocal(), "imp016") {
		t.Fatalf("zone: %q", a.SiteLocal())
	}
	if got := a.MonitorCount(a.Config().Config); got != 300 {
		t.Fatalf("count after the import: %d, want 300", got)
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

// The master keeps a copy of a node's own configuration (never applied,
// counted toward no limit).
func TestMasterKeepsSiteLocalReports(t *testing.T) {
	a, _, _, dir := newApp(t, capConfig(50)+"b IN NODE Beijing\n")
	defer a.Store().Close()
	text := localZone(30, "bj")
	a.onSiteLocalReport("b", 30, "h1", &text)
	if b, err := os.ReadFile(filepath.Join(dir, "sites", "b."+SiteLocalFile)); err != nil || string(b) != text {
		t.Fatalf("copy: %v %q", err, b)
	}
	if got := a.MonitorCount(a.Config().Config); got != 50 {
		t.Fatalf("count: %d, want 50", got)
	}
}
