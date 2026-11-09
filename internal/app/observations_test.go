package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/observation"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func observationSite(t *testing.T, node string) *App {
	t.Helper()
	a, _, _, dir := newApp(t, testConfig+"b IN NODE Beijing\nc IN NODE Tokyo\n")
	a.am.member = &Membership{Node: node, Server: "https://master.invalid", Fingerprint: "unused"}
	os.WriteFile(filepath.Join(dir, agentFile), mustJSON(a.am.member), 0600)
	a.replica.cfg = a.applied.Load()
	a.replica.local = true
	t.Cleanup(func() { a.store.Close() })
	return a
}
func observationServer(t *testing.T, a *App) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+SiteObservationPath, a.ServeSiteObservations)
	mux.HandleFunc("POST "+SiteIncidentActionPath, a.ServeSiteIncidentAction)
	s := httptest.NewUnstartedServer(mux)
	s.TLS = a.pki.TLSConfig(nil)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}
func rememberObservationPeer(t *testing.T, a, peer *App, server *httptest.Server) cluster.PeerAddr {
	t.Helper()
	p := cluster.PeerAddr{Name: peer.nodeName(), Addr: server.Listener.Addr().String(), Fingerprint: peer.pki.PeerIdentity().Fingerprint()}
	if err := writePeerAtomic(a.dataPath(peerIdentityFile), mustJSON([]cluster.PeerAddr{p})); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestObservationSyncWithoutMaster(t *testing.T) {
	b, c := observationSite(t, "b"), observationSite(t, "c")
	bs, cs := observationServer(t, b), observationServer(t, c)
	pb := rememberObservationPeer(t, c, b, bs)
	pc := rememberObservationPeer(t, b, c, cs)
	hash := b.EffectiveConfig().FindMonitor("web").DefHash
	if err := b.store.RecordSiteObservation(observation.Row{NodeID: "b", MonitorID: "web", Status: "down", CheckedAt: time.Now(), IntervalMS: 60000, DefHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := c.pullSiteObservations(context.Background(), pb); err != nil {
		t.Fatal("master-independent pull", err)
	}
	snap, err := c.store.SiteObservationSnapshot("web", hash, []string{"unagikuma", "b", "c"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("missing snapshot")
	}
	found := false
	for _, r := range snap.Rows {
		if r.NodeID == "b" && r.Status == "down" && !r.Stale {
			found = true
		}
	}
	if !found {
		t.Fatal(snap)
	}
	if err := c.store.RecordSiteObservation(observation.Row{NodeID: "c", MonitorID: "web", Status: "up", CheckedAt: time.Now(), IntervalMS: 60000, DefHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := b.pullSiteObservations(context.Background(), pc); err != nil {
		t.Fatal(err)
	}
	// An anonymous client, despite pinning the server correctly, is not a peer.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cluster.PinnedPeerTLSConfig(pb.Fingerprint)}}
	defer client.CloseIdleConnections()
	res, err := client.Get(bs.URL + SiteObservationPath + "?after=0")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("anonymous peer accepted", res.StatusCode)
	}
	// A different site's private key cannot claim the known source name.
	other, err := cluster.LoadOrCreatePKI(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := cluster.PinnedPeerTLSConfig(pb.Fingerprint)
	cfg.Certificates = []tls.Certificate{other.Leaf}
	forged := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	defer forged.CloseIdleConnections()
	req, _ := http.NewRequest("GET", bs.URL+SiteObservationPath+"?after=0", nil)
	req.Header.Set(peerSiteHeader, "c")
	res, err = forged.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("forged identity accepted", res.StatusCode)
	}
}

func TestObservationPullDrainsMultiplePages(t *testing.T) {
	var extra strings.Builder
	extra.WriteString("b IN NODE Beijing\nc IN NODE Tokyo\n")
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&extra, "check-%03d IN HTTP http://127.0.0.1/\n", i)
	}
	makeSite := func(node string) *App {
		a, _, _, _ := newApp(t, testConfig+extra.String())
		a.am.member = &Membership{Node: node, Server: "https://master.invalid"}
		a.replica.cfg = a.applied.Load()
		a.replica.local = true
		t.Cleanup(func() { a.store.Close() })
		return a
	}
	b, c := makeSite("b"), makeSite("c")
	bs, cs := observationServer(t, b), observationServer(t, c)
	peer := rememberObservationPeer(t, c, b, bs)
	rememberObservationPeer(t, b, c, cs)
	for i := 0; i < 600; i++ {
		name := fmt.Sprintf("check-%03d", i)
		if err := b.store.RecordSiteObservation(observation.Row{NodeID: "b", MonitorID: name, Status: "down", CheckedAt: time.Now(), IntervalMS: 60000, DefHash: b.EffectiveConfig().FindMonitor(name).DefHash}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.pullSiteObservations(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	_, seq := c.store.SiteObservationCursor("b")
	if seq != 600 {
		t.Fatalf("only first page synchronized: %d", seq)
	}
}
