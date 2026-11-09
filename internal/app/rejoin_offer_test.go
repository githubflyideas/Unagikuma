package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/store"
)

func TestRejoinOfferUsesAdvertiseURLForUnspecifiedListen(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{DataDir: dir, Listen: ":8077", Probe: (&fakeProbes{}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	a.savePromoted(promoted{Self: "b", OldMaster: "a", Term: 2, Base: store.Snapshot{Serial: 1, SHA256: "x"}})
	// Ensure NODE a exists so prevSite works via OldMaster.
	n := cluster.RetireNotice{Term: 2, At: time.Now()}
	a.rejoinOffer(&n)
	if n.Join == "" || n.Node != "a" {
		t.Fatalf("expected join offer for a, got join=%q node=%q", n.Join, n.Node)
	}
	info, err := cluster.DecodeJoin(n.Join)
	if err != nil {
		t.Fatal(err)
	}
	if info.Server == "" || info.Server == "https://:8077" || info.Server == "https://0.0.0.0:8077" {
		t.Fatalf("bad server in join: %q", info.Server)
	}
}

func TestIssueRejoinOfferRequiresKnownNode(t *testing.T) {
	dir := t.TempDir()
	cfg := `@ IN SOA t. admin.localhost. ( 1 1m 15s 5s 3 )
a IN NODE "A"
`
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{DataDir: dir, Listen: "127.0.0.1:18077", Probe: (&fakeProbes{}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	a.savePromoted(promoted{Self: a.Config().Config.MName, OldMaster: "a", Term: 2})
	n, err := a.IssueRejoinOffer("a")
	if err != nil || n.Join == "" || n.Node != "a" {
		t.Fatalf("offer %v %+v", err, n)
	}
	if _, err := a.IssueRejoinOffer("nosuch"); err == nil {
		t.Fatal("expected reject")
	}
}

func TestSignAndCheckRejoin(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	req, err := a.PKI().SignRejoin("a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PKI().CheckRejoin(req); err != nil {
		t.Fatal(err)
	}
	req.At = time.Now().Add(-10 * time.Minute)
	if err := a.PKI().CheckRejoin(req); err == nil {
		t.Fatal("stale must fail")
	}
}
