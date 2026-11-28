package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/license"
	"github.com/githubflyideas/unagikuma/internal/observation"
)

// nodeConfig is testConfig with n nodes besides the master.
func nodeConfig(n int) string {
	var b strings.Builder
	b.WriteString(testConfig)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "n%d IN NODE \"site %d\"\n", i, i)
	}
	return b.String()
}

// installNodeLicense installs a signed licence (made out for any
// installation) and applies it.
func installNodeLicense(t *testing.T, a *App, f license.File) {
	t.Helper()
	f.Customer, f.Licensee, f.Product = "Test", license.LicenseeOrganization, license.Product
	if f.License == "" {
		f.License = license.TypePerpetual
	}
	b, _ := json.Marshal(license.Sign(testLicenseKey, f))
	if _, err := a.License().Install(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.LicenseChanged()
}

func newNodeApp(t *testing.T, text string) *App {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ring := license.SingleKey(&testLicenseKey.PublicKey)
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run, Instance: "test", LicenseRing: &ring})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Store().Close() })
	return a
}

func TestCountNodes(t *testing.T) {
	_, none, _ := config.Load("monitors.conf", []byte(testConfig), config.Options{})
	if got := CountNodes(none); got != 1 {
		t.Fatalf("one machine: %d, want 1", got)
	}
	_, three, d := config.Load("monitors.conf", []byte(nodeConfig(2)), config.Options{})
	if config.HasErrors(d) {
		t.Fatal(d)
	}
	// The master is one of the nodes; its own NODE record, if any, is not
	// counted twice.
	if got := CountNodes(three); got != 3 {
		t.Fatalf("master and two nodes: %d, want 3", got)
	}
	if got := CountNodes(nil); got != 1 {
		t.Fatalf("no configuration: %d, want 1", got)
	}
}

func TestNodeLimit(t *testing.T) {
	for _, c := range []struct {
		name string
		st   license.State
		want int
	}{
		{"unregistered", license.State{}, 3},
		{"expired", license.State{Kind: license.KindExpired, MaxNodes: 50}, 3},
		{"licence without a node limit", license.State{Registered: true}, 0},
		{"limit above the free number", license.State{Registered: true, MaxNodes: 8}, 8},
		{"never below the free number", license.State{Registered: true, MaxNodes: 2}, 3},
	} {
		if got := NodeLimit(c.st); got != c.want {
			t.Errorf("%s: limit %d, want %d", c.name, got, c.want)
		}
	}
}

func TestAllowAddNodes(t *testing.T) {
	free := license.State{}
	if err := allowAdd(free, Usage{Nodes: 2}, "nodes"); err != nil {
		t.Fatalf("third node: %v", err)
	}
	err := allowAdd(free, Usage{Nodes: 3}, "nodes")
	var qe *QuotaError
	if !errors.As(err, &qe) || !errors.Is(err, ErrQuota) || qe.Kind != "nodes" || qe.Limit != 3 || qe.Used != 3 {
		t.Fatalf("fourth node: %v", err)
	}
	if !strings.Contains(err.Error(), "3 nodes") || !strings.Contains(err.Error(), "master") {
		t.Fatalf("message: %q", err)
	}
	// A licence of an older format (no node limit) allows any number.
	if err := allowAdd(license.State{Registered: true}, Usage{Nodes: 40}, "nodes"); err != nil {
		t.Fatalf("licence without a node limit: %v", err)
	}
	if err := allowAdd(license.State{Registered: true, MaxNodes: 5}, Usage{Nodes: 5}, "nodes"); err == nil {
		t.Fatal("sixth node under a limit of 5")
	}
	if err := allowAdd(license.State{Registered: true, MaxNodes: 5}, Usage{Nodes: 4}, "nodes"); err != nil {
		t.Fatalf("fifth node under a limit of 5: %v", err)
	}
	// Accounts are unaffected by the node rule.
	if err := allowAdd(free, Usage{Users: 4, Nodes: 9}, "users"); err != nil {
		t.Fatalf("fifth account: %v", err)
	}
}

// The unlicensed limit is three nodes, the master counted: the fourth
// machine is refused, joining or added by hand, and nothing already
// there is dropped.
func TestNodeLimitUnlicensed(t *testing.T) {
	a := newNodeApp(t, nodeConfig(2)) // master, n1, n2: three
	if u := a.Usage(); u.Nodes != 3 {
		t.Fatalf("usage: %+v", u)
	}
	if err := a.AllowAdd("nodes"); err == nil {
		t.Fatal("a fourth node is allowed")
	}
	add := func(name string) error {
		_, err := a.EditConfig("test", "add", func(f *config.File) error {
			return f.UpsertRecord(config.RecordSpec{Name: name, Type: "NODE", Target: name})
		})
		return err
	}
	err := add("n3")
	var ie *InvalidError
	if !errors.As(err, &ie) || len(ie.Diags) != 1 || ie.Diags[0].Code != "node_limit" {
		t.Fatalf("fourth node by hand: %v", err)
	}
	if got := a.Config().Config.FindNode("n3"); got != nil {
		t.Fatal("the refused node is in the configuration")
	}
	// A node already there is edited freely, and one can be removed; the
	// room it leaves is usable again.
	if _, err := a.EditConfig("test", "retitle", func(f *config.File) error {
		return f.UpsertRecord(config.RecordSpec{Name: "n1", Type: "NODE", Target: "renamed"})
	}); err != nil {
		t.Fatalf("edit at the limit: %v", err)
	}
	if _, err := a.EditConfig("test", "del", func(f *config.File) error { return f.DeleteRecord("n2") }); err != nil {
		t.Fatalf("delete at the limit: %v", err)
	}
	if err := add("n3"); err != nil {
		t.Fatalf("a node after one was removed: %v", err)
	}
}

// A join code does not get a fourth node in either: the quota is checked
// before the code is spent.
func TestNodeLimitJoinRefused(t *testing.T) {
	a := newNodeApp(t, nodeConfig(2))
	code, _, err := a.Store().CreateJoinCode(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := cluster.JoinRequest{Code: code, Node: "n9"}
	_, err = a.AcceptJoin(req)
	var qe *QuotaError
	if !errors.As(err, &qe) || qe.Kind != "nodes" {
		t.Fatalf("fourth machine joining: %v", err)
	}
	if a.Config().Config.FindNode("n9") != nil {
		t.Fatal("the refused machine is a node")
	}
	// The code was not spent: with room it works.
	if _, err := a.EditConfig("test", "del", func(f *config.File) error { return f.DeleteRecord("n2") }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcceptJoin(req); err != nil {
		t.Fatalf("join with room: %v", err)
	}
}

// A licence with max_nodes lifts the limit to its own; one that states
// less than the free number gives the free number; an expired one is the
// free limit again, and what is there keeps running.
func TestNodeLimitLicensed(t *testing.T) {
	a := newNodeApp(t, nodeConfig(2))
	installNodeLicense(t, a, license.File{MaxNodes: 5})
	if c := a.NodeCap(); c.Limit != 5 || c.Used != 3 || c.Over || !c.Registered {
		t.Fatalf("cap: %+v", c)
	}
	for _, n := range []string{"n3", "n4"} {
		if _, err := a.EditConfig("test", "add", func(f *config.File) error {
			return f.UpsertRecord(config.RecordSpec{Name: n, Type: "NODE", Target: n})
		}); err != nil {
			t.Fatalf("%s under a limit of 5: %v", n, err)
		}
	}
	// Master and four nodes are five: the limit.
	if err := a.AllowAdd("nodes"); err == nil {
		t.Fatal("a sixth machine under a limit of 5")
	}
	if _, err := a.EditConfig("test", "add", func(f *config.File) error {
		return f.UpsertRecord(config.RecordSpec{Name: "n5", Type: "NODE", Target: "n5"})
	}); err == nil {
		t.Fatal("a sixth machine by hand under a limit of 5")
	}

	// The licence ends: the five stay, a sixth is refused, and the standing
	// says it is over the free three.
	exp := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	installNodeLicense(t, a, license.File{MaxNodes: 8, Expires: &exp, License: license.TypeSubscription})
	if err := a.AllowAdd("nodes"); err != nil {
		t.Fatalf("sixth node under a limit of 8: %v", err)
	}
	if st := a.License().Check(time.Now().AddDate(0, 0, 30)); st.Kind != license.KindExpired || st.Registered {
		t.Fatalf("licence not expired: %+v", st)
	}
	if err := a.AllowAdd("nodes"); err == nil {
		t.Fatal("a node after the licence expired")
	}
	if c := a.NodeCap(); c.Limit != 3 || c.Used != 5 || !c.Over || c.Registered {
		t.Fatalf("expired cap: %+v", c)
	}
	if got := len(a.Config().Config.Nodes); got < 4 { // the four besides the master
		t.Fatalf("nodes dropped after expiry: %d", got)
	}
	// Taking one more node by hand is refused too, but a change that does
	// not grow the count is not.
	if _, err := a.EditConfig("test", "add", func(f *config.File) error {
		return f.UpsertRecord(config.RecordSpec{Name: "n5", Type: "NODE", Target: "n5"})
	}); err == nil {
		t.Fatal("a node by hand after expiry")
	}
	if _, err := a.EditConfig("test", "retitle", func(f *config.File) error {
		return f.UpsertRecord(config.RecordSpec{Name: "n1", Type: "NODE", Target: "again"})
	}); err != nil {
		t.Fatalf("edit over the limit: %v", err)
	}

	// A limit below the free number is the free number.
	installNodeLicense(t, a, license.File{MaxNodes: 2})
	if c := a.NodeCap(); c.Limit != 3 {
		t.Fatalf("licence limit 2: %+v", c)
	}
	// And no field is no limit.
	installNodeLicense(t, a, license.File{})
	if c := a.NodeCap(); c.Limit != 0 || c.Over {
		t.Fatalf("licence without max_nodes: %+v", c)
	}
	if err := a.AllowAdd("nodes"); err != nil {
		t.Fatalf("node under an unlimited licence: %v", err)
	}
}

// The diag follows the monitor rule: only growth past the limit and past
// what is there is refused.
func TestNodeCapDiag(t *testing.T) {
	a := newNodeApp(t, testConfig)
	load := func(n int) *config.Config {
		_, c, d := config.Load("monitors.conf", []byte(nodeConfig(n)), config.Options{})
		if config.HasErrors(d) {
			t.Fatal(d)
		}
		return c
	}
	n3, n4, n5, n6 := load(2), load(3), load(4), load(5)
	if d := a.nodeCapDiag(n3, n4); d == nil || d.Code != "node_limit" {
		t.Fatalf("grow past the limit: %+v", d)
	}
	if d := a.nodeCapDiag(n3, n3); d != nil {
		t.Fatalf("hold at the limit: %+v", d)
	}
	if d := a.nodeCapDiag(n5, n5); d != nil {
		t.Fatalf("hold over the limit: %+v", d)
	}
	if d := a.nodeCapDiag(n6, n5); d != nil {
		t.Fatalf("shrink over the limit: %+v", d)
	}
	if d := a.nodeCapDiag(n5, n6); d == nil {
		t.Fatal("grow over the limit accepted")
	}
	if d := a.capDiag(n3, n4, true); d == nil || d.Code != "node_limit" {
		t.Fatalf("an import's exemption covers monitors only: %+v", d)
	}
}

// The file path behaves like the monitor limit: a file grown past the
// node limit while the program was stopped is not applied, the last
// version runs on and a banner says why. A first start runs the file as
// it is, and a cluster already past the limit keeps running.
func TestStartupHoldsNodeLimit(t *testing.T) {
	a, _, _, dir := newApp(t, nodeConfig(2))
	a.Store().Close()
	grown := strings.Replace(nodeConfig(3), "2026102301", "2026102302", 1)
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(grown), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Store().Close()
	if got := CountNodes(b.Config().Config); got != 3 {
		t.Fatalf("nodes after a restart past the limit: %d, want the last 3", got)
	}
	if bn := b.Banner(); bn == nil || bn.Code != "node_limit" {
		t.Fatalf("banner: %+v", bn)
	}

	// First start: nothing to compare with, the file runs as it is.
	c, _, _, _ := newApp(t, nodeConfig(5))
	defer c.Store().Close()
	if got := CountNodes(c.Config().Config); got != 6 {
		t.Fatalf("a first start with six nodes ran %d", got)
	}
	if nc := c.NodeCap(); !nc.Over || nc.Used != 6 || nc.Limit != 3 {
		t.Fatalf("standing: %+v", nc)
	}
}

// The site view is always on: no licence, no trial, no clock. An alert
// and the console carry what every other site sees.
func TestSiteViewAlwaysOn(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig+"b IN NODE Beijing\nc IN NODE Tokyo\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.engine.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
		a.Store().Close()
	}()
	if a.License().State().Registered {
		t.Fatal("test needs an unregistered installation")
	}
	hash := a.EffectiveConfig().FindMonitor("web").DefHash
	if err := a.store.RecordSiteObservation(observation.Row{NodeID: "b", MonitorID: "web", Status: "down", CheckedAt: time.Now(), IntervalMS: 60000, DefHash: hash}); err != nil {
		t.Fatal(err)
	}
	if snap := a.ObservationSnapshot("web"); snap == nil || len(snap.Rows) == 0 {
		t.Fatalf("unregistered installation, no site view: %+v", snap)
	}
	// A trial that an older release started and that would long have ended
	// changes nothing.
	old := time.Now().UTC().AddDate(0, -1, 0).Format(time.RFC3339)
	if err := a.store.SetSetting("site_view.trial_start", old); err != nil {
		t.Fatal(err)
	}
	if snap := a.ObservationSnapshot("web"); snap == nil || len(snap.Rows) == 0 {
		t.Fatalf("site view stopped after the old trial: %+v", snap)
	}
	s := &sink{a: a, Store: a.Store()}
	if snap, err := s.SiteObservationSnapshot("web", hash, []string{"b"}, time.Now()); err != nil || snap == nil {
		t.Fatalf("an alert carries no sites: %v %v", snap, err)
	}
}
