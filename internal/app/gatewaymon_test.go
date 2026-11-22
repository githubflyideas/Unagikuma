package app

import (
	"testing"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// The internet gateway gets a ping monitor once; it follows the address,
// keeps the user's settings and stays deleted once deleted.
func TestGatewayMonitors(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.store.Close()
	setExit := func(addr string) {
		t.Helper()
		if _, err := a.EditConfig("alice", "exit", func(f *config.File) error {
			return f.SetTunables(map[string]string{"localnet.exit-target": addr}, nil)
		}); err != nil {
			t.Fatal(err)
		}
		a.ensureGatewayMonitors()
	}
	record := func() (config.Record, bool) {
		f, _ := config.Parse("x", []byte(a.Config().Text))
		return f.Record("internet-gateway")
	}
	setExit("114.114.114.114")
	r, ok := record()
	if !ok || r.Type != "PING" || r.Target != "114.114.114.114" || r.Interval != "1m" {
		t.Fatalf("record %+v %v", r, ok)
	}
	// The user raises the attempts; the gateway moves; the attempts stay.
	if _, err := a.EditConfig("alice", "attempts", func(f *config.File) error {
		return f.SetOpts("internet-gateway", map[string]string{"attempts": "3"})
	}); err != nil {
		t.Fatal(err)
	}
	setExit("223.5.5.5")
	r, _ = record()
	attempts := ""
	for _, o := range r.Opts {
		if o.Key == "attempts" {
			attempts = o.Value
		}
	}
	if r.Target != "223.5.5.5" || attempts != "3" {
		t.Fatalf("after the move %+v", r)
	}
	// Deleted by the user: not added again, even for another address.
	if _, err := a.EditConfig("alice", "delete", func(f *config.File) error { return f.DeleteRecord("internet-gateway") }); err != nil {
		t.Fatal(err)
	}
	setExit("8.8.8.8")
	if _, ok := record(); ok {
		t.Fatal("added again after it was deleted")
	}
}

// Seeding a gateway monitor into an installation that already has
// monitors of its own is announced: a system event and an audit entry
// say what appeared. A gateway that only moves does not announce
// again, and the seeded monitor takes its fixed rank at the head of
// the list.
func TestGatewayMonitorAdditionIsAnnounced(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.store.Close()
	setExit := func(addr string) {
		t.Helper()
		if _, err := a.EditConfig("alice", "exit", func(f *config.File) error {
			return f.SetTunables(map[string]string{"localnet.exit-target": addr}, nil)
		}); err != nil {
			t.Fatal(err)
		}
		a.ensureGatewayMonitors()
	}
	notices := func() []store.EventRow {
		t.Helper()
		a.Store().Flush()
		p, err := a.Store().Events(store.EventFilter{Monitor: "internet-gateway", Types: []string{engine.EventSystem}})
		if err != nil {
			t.Fatal(err)
		}
		var out []store.EventRow
		for _, ev := range p.Items {
			if ev.Data["msg"] == "gateway_monitor_added" {
				out = append(out, ev)
			}
		}
		return out
	}
	setExit("114.114.114.114")
	evs := notices()
	if len(evs) != 1 || evs[0].Data["target"] != "114.114.114.114" {
		t.Fatalf("notices %+v", evs)
	}
	aud, err := a.Store().AuditLog(store.AuditQuery{Action: "system.gateway_monitor_added", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, e := range aud {
		if e.Actor == "system" && e.Detail["monitor"] == "internet-gateway" {
			mine++
		}
	}
	if mine != 1 {
		t.Fatalf("audit entries for the addition: %d of %+v", mine, aud)
	}
	if r := a.GatewayRank("internet-gateway"); r != 1 {
		t.Fatalf("internet gateway rank %d", r)
	}
	if r := a.GatewayRank("web"); r != -1 {
		t.Fatalf("user monitor rank %d", r)
	}
	// The gateway moves: the monitor follows, no second announcement.
	setExit("223.5.5.5")
	if evs := notices(); len(evs) != 1 {
		t.Fatalf("notices after the move %+v", evs)
	}
}

// A new installation — no monitors of its own yet — is not seeded, is
// told nothing, and ranks nothing.
func TestGatewayMonitorsLeaveNewInstallationsAlone(t *testing.T) {
	a, _, _, _ := newApp(t, `@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s suspend.recheck-spread=0s notify.repeat=1m
logc    IN CHANNEL log
default IN POLICY chain=logc max=3
`)
	defer a.store.Close()
	a.ensureGatewayMonitors()
	if _, ok := a.Config().File.Record("internet-gateway"); ok {
		t.Fatal("a new installation was seeded")
	}
	a.Store().Flush()
	p, err := a.Store().Events(store.EventFilter{Types: []string{engine.EventSystem}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range p.Items {
		if ev.Data["msg"] == "gateway_monitor_added" {
			t.Fatalf("a new installation was told %+v", ev)
		}
	}
	if r := a.GatewayRank("internet-gateway"); r != -1 {
		t.Fatalf("rank without seeding %d", r)
	}
}

// The reports leave out the gateway monitors the installation adds: the
// engine's list must name every one of them.
func TestReportsKnowTheGatewayMonitors(t *testing.T) {
	for _, g := range gatewayMonitors {
		if !engine.SystemMonitors[g.name] {
			t.Errorf("%s is not in engine.SystemMonitors", g.name)
		}
	}
}
