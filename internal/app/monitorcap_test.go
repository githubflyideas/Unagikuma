package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/license"
)

// capConfig builds a configuration with n global ping monitors.
func capConfig(n int) string {
	var b strings.Builder
	b.WriteString("@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )\n")
	b.WriteString("logc    IN CHANNEL log\ndefault IN POLICY chain=logc max=3\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "m%03d IN PING 10.0.0.%d\n", i, i%250+1)
	}
	return b.String()
}

func TestCountMasterMonitors(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig+`bj IN NODE "北京" region=north
sh IN NODE "上海" region=east
only-bj     IN PING 10.10.0.1 nodes=bj
only-master IN PING 10.10.0.2 nodes=unagikuma
$SCOPE region:north
north-mon   IN PING 10.10.0.3
$SCOPE *
`)
	defer a.Store().Close()
	// web, dns (global), backup (push: received at the master) and
	// only-master count; only-bj and north-mon run elsewhere.
	if got := a.MonitorCount(a.Config().Config); got != 4 {
		t.Fatalf("count: %d, want 4", got)
	}
	if !AreaInMasterSet("", a.Config().Config) || !AreaInMasterSet("*", a.Config().Config) ||
		AreaInMasterSet("region:north", a.Config().Config) || !AreaInMasterSet("unagikuma", a.Config().Config) ||
		AreaInMasterSet("bj", a.Config().Config) {
		t.Fatal("area placement")
	}
	// The seeded gateway monitors are not the user's: with their
	// markers set, records of their names stop counting.
	if err := a.Store().SetSetting("auto_monitor_lan_gateway", "192.168.1.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EditConfig("test", "add gateways", func(f *config.File) error {
		if err := f.UpsertRecord(config.RecordSpec{Name: "lan-gateway", Type: "PING", Target: "192.168.1.1"}); err != nil {
			return err
		}
		return f.UpsertRecord(config.RecordSpec{Name: "internet-gateway", Type: "PING", Target: "203.0.113.1"})
	}); err != nil {
		t.Fatal(err)
	}
	// internet-gateway has no marker yet, so it counts; lan-gateway does not.
	if got := a.MonitorCount(a.Config().Config); got != 5 {
		t.Fatalf("count with gateways: %d, want 5", got)
	}
	if err := a.Store().SetSetting("auto_monitor_internet_gateway", "203.0.113.1"); err != nil {
		t.Fatal(err)
	}
	if got := a.MonitorCount(a.Config().Config); got != 4 {
		t.Fatalf("count with both gateways seeded: %d, want 4", got)
	}
}

func TestMonitorLimitValues(t *testing.T) {
	if got := MonitorLimit(license.State{}); got != license.FreeMonitors {
		t.Fatalf("unregistered: %d", got)
	}
	if got := MonitorLimit(license.State{Registered: true}); got != 0 {
		t.Fatalf("registered, no field: %d (want no limit)", got)
	}
	if got := MonitorLimit(license.State{Registered: true, MaxMonitors: 150}); got != 150 {
		t.Fatalf("registered 150: %d", got)
	}
	if got := MonitorLimit(license.State{Registered: true, MaxMonitors: 50}); got != license.FreeMonitors {
		t.Fatalf("a licence never gives fewer than free: %d", got)
	}
}

// The ratchet: at the free limit an in-place edit passes and one more
// monitor is refused with the monitor_limit diagnostic; over the limit
// (as an import can leave an installation) edits and removals pass,
// growth does not, and at the limit growth opens again below it.
func TestMonitorCapRatchet(t *testing.T) {
	a, _, _, _ := newApp(t, capConfig(100))
	defer a.Store().Close()
	if got := a.MonitorCount(a.Config().Config); got != 100 {
		t.Fatalf("count: %d", got)
	}
	add := func(name string) error {
		_, err := a.EditConfig("test", "add", func(f *config.File) error {
			return f.UpsertRecord(config.RecordSpec{Name: name, Type: "PING", Target: "10.9.9.9"})
		})
		return err
	}
	if err := add("one-more"); err == nil {
		t.Fatal("101st monitor accepted")
	} else {
		var ie *InvalidError
		if !errors.As(err, &ie) || len(ie.Diags) != 1 || ie.Diags[0].Code != "monitor_limit" {
			t.Fatalf("refusal: %v", err)
		}
	}
	// In-place edit at the limit.
	if _, err := a.EditConfig("test", "edit", func(f *config.File) error {
		return f.UpsertRecord(config.RecordSpec{Name: "m001", Type: "PING", Target: "10.1.1.1"})
	}); err != nil {
		t.Fatalf("in-place edit at the limit: %v", err)
	}
	// The diag itself, over the limit: 120 running.
	_, cfg120, d := config.Load("monitors.conf", []byte(capConfig(120)), config.Options{})
	if config.HasErrors(d) {
		t.Fatalf("load 120: %v", d)
	}
	_, cfg121, _ := config.Load("monitors.conf", []byte(capConfig(121)), config.Options{})
	_, cfg119, _ := config.Load("monitors.conf", []byte(capConfig(119)), config.Options{})
	if got := a.monitorCapDiag(cfg120, cfg121); got == nil || got.Code != "monitor_limit" {
		t.Fatalf("grow past cap and current: %+v", got)
	}
	if got := a.monitorCapDiag(cfg120, cfg120); got != nil {
		t.Fatalf("hold over the cap: %+v", got)
	}
	if got := a.monitorCapDiag(cfg120, cfg119); got != nil {
		t.Fatalf("shrink over the cap: %+v", got)
	}
	if got := a.monitorCapDiag(cfg119, cfg120); got == nil {
		t.Fatal("grow back to 120 from 119 accepted")
	}
	// Down at the limit, room opens below it.
	if _, err := a.EditConfig("test", "del", func(f *config.File) error { return f.DeleteRecord("m100") }); err != nil {
		t.Fatalf("delete at the limit: %v", err)
	}
	if err := add("back"); err != nil {
		t.Fatalf("add back to the limit: %v", err)
	}
}

// The style trial: not started, running with days left, ended — and
// starting it twice keeps the first start.
func TestStyleTrialStates(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	if tr := a.StyleTrial(); tr.Started || tr.Active || tr.Ended {
		t.Fatalf("fresh: %+v", tr)
	}
	a.StartStyleTrial()
	if tr := a.StyleTrial(); !tr.Active || tr.DaysLeft != StyleTrialDays {
		t.Fatalf("started: %+v", tr)
	}
	past := a.now().AddDate(0, 0, -StyleTrialDays-1).UTC().Format(time.RFC3339)
	if err := a.Store().SetSetting(styleTrialKey, past); err != nil {
		t.Fatal(err)
	}
	if tr := a.StyleTrial(); !tr.Ended || tr.Active {
		t.Fatalf("day 8: %+v", tr)
	}
	a.StartStyleTrial() // an ended trial does not restart
	if tr := a.StyleTrial(); !tr.Ended {
		t.Fatalf("restarted: %+v", tr)
	}
}
