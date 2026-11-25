package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

// One site asks another to check monitors now. Only a site that proves its
// identity is heard; only monitors defined the same way and checked by the
// asked site are checked.
func TestPeerRecheckRequest(t *testing.T) {
	b, c := observationSite(t, "b"), observationSite(t, "c")
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PeerRecheckPath, b.ServePeerRecheck)
	bs := observationServerWith(t, b, mux)
	pb := rememberObservationPeer(t, c, b, bs)
	rememberObservationPeer(t, b, c, observationServer(t, c)) // b knows c's key
	hash := b.EffectiveConfig().FindMonitor("web").DefHash

	ask := func(client *http.Client, items []RecheckItem) (int, int) {
		t.Helper()
		body, _ := json.Marshal(items)
		req, _ := http.NewRequest("POST", bs.URL+PeerRecheckPath, bytes.NewReader(body))
		req.Header.Set(peerSiteHeader, "c")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct{ Started int }
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out.Started
	}
	peer := c.peerClient(pb)
	defer peer.CloseIdleConnections()
	// A site that is not monitoring yet answers at once, without waiting.
	start := time.Now()
	if code, _ := ask(peer, []RecheckItem{{Name: "web", DefHash: hash}}); code != 503 || time.Since(start) > time.Second {
		t.Fatalf("not running: %d after %v", code, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.engine.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for !b.engine.Running() {
		time.Sleep(time.Millisecond)
	}
	// A definition that differs, or a monitor unknown here: nothing.
	if code, n := ask(peer, []RecheckItem{{Name: "web", DefHash: "other"}, {Name: "nope", DefHash: hash}}); code != 200 || n != 0 {
		t.Fatalf("mismatched: %d %d", code, n)
	}
	// The same definition: checked now.
	if code, n := ask(peer, []RecheckItem{{Name: "web", DefHash: hash}}); code != 200 || n != 1 {
		t.Fatalf("same definition: %d %d", code, n)
	}
	// Asked again at once: the engine's own rate limit holds it back.
	if _, n := ask(peer, []RecheckItem{{Name: "web", DefHash: hash}}); n != 0 {
		t.Fatalf("rate limit: %d", n)
	}
	// Anyone without a site's key is refused.
	anon := &http.Client{Transport: &http.Transport{TLSClientConfig: cluster.PinnedPeerTLSConfig(pb.Fingerprint)}}
	defer anon.CloseIdleConnections()
	if code, _ := ask(anon, []RecheckItem{{Name: "web", DefHash: hash}}); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

// The engine's hook never blocks: a full queue drops the request. A monitor
// asks again only after the cooldown, so a flapping link is not a storm.
func TestPeerRecheckQueueAndCooldown(t *testing.T) {
	a := observationSite(t, "b")
	a.rechecks = &peerRechecker{q: make(chan recheckRequest, 1), last: map[string]time.Time{}}
	done := make(chan struct{})
	go func() {
		a.firstFailure("web", "h", []string{"b", "c"})
		a.firstFailure("web", "h", []string{"b", "c"}) // queue full: dropped, not waited for
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the hook blocked")
	}
	req := <-a.rechecks.q
	a.sendPeerRechecks(t.Context(), []recheckRequest{req})
	first := a.rechecks.last["web"]
	if first.IsZero() {
		t.Fatal("not recorded")
	}
	a.sendPeerRechecks(t.Context(), []recheckRequest{req})
	if !a.rechecks.last["web"].Equal(first) {
		t.Fatal("asked again within the cooldown")
	}
}
