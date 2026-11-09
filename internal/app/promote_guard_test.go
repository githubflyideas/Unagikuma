package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

func TestCompetingMasterDetectsHigherTerm(t *testing.T) {
	dir := t.TempDir()
	a := &App{opts: Options{DataDir: dir}}
	a.pki, _ = cluster.LoadOrCreatePKI(dir, time.Now())
	mux := http.NewServeMux()
	mux.HandleFunc(cluster.ReachPath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cluster.ReachView{Node: "b", Role: "master", Term: 3})
	})
	srv := trustedReachServer(t, a, mux)
	defer srv.Close()
	host := srv.Listener.Addr().String()
	if err := os.WriteFile(filepath.Join(dir, peersFile), mustJSON([]string{host}), 0o644); err != nil {
		t.Fatal(err)
	}
	peer, term, ok := a.competingMaster(2)
	if !ok || peer != "b" || term != 3 {
		t.Fatalf("got peer=%q term=%d ok=%v", peer, term, ok)
	}
	_, _, ok = a.competingMaster(4)
	if ok {
		t.Fatal("term 4 should not match a master at 3")
	}
}

func TestPromoteRefusedWhenPeerMasterExists(t *testing.T) {
	prev := promoteDelayFn
	promoteDelayFn = func() time.Duration { return 0 }
	defer func() { promoteDelayFn = prev }()

	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	meta := StandbyState{Term: 1, Key: "k", Fetched: time.Now()}
	if err := os.MkdirAll(a.dataPath(standbyDir), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(a.dataPath(standbyDir, stateMetaFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.dataPath(standbyDir, stateFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(cluster.ReachPath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cluster.ReachView{Node: "other", Role: "master", Term: 2})
	})
	srv := trustedReachServer(t, a, mux)
	defer srv.Close()
	if err := os.WriteFile(a.dataPath(peersFile), mustJSON([]string{srv.Listener.Addr().String()}), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := a.Promote(context.Background(), 0, "tester")
	if err == nil {
		t.Fatal("expected refuse")
	}
	var refused *PromoteRefused
	if !errors.As(err, &refused) || refused.Peer != "other" || refused.Term != 2 {
		t.Fatalf("got %v", err)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func trustedReachServer(t *testing.T, a *App, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = a.pki.TLSConfig(nil)
	srv.StartTLS()
	return srv
}

func TestReachRejectsForeignTLSAndWrongNode(t *testing.T) {
	dir := t.TempDir()
	pki, err := cluster.LoadOrCreatePKI(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{opts: Options{DataDir: dir}, pki: pki}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster.ServeReach(w, cluster.ReachView{Node: "forged", Role: "master", Term: 999})
	})
	foreign := httptest.NewTLSServer(h)
	defer foreign.Close()
	if _, ok := a.probeReach(foreign.Listener.Addr().String()); ok {
		t.Fatal("accepted foreign TLS authority")
	}
	trusted := trustedReachServer(t, a, h)
	defer trusted.Close()
	addr := trusted.Listener.Addr().String()
	b, _ := json.Marshal([]cluster.PeerAddr{{Name: "expected", Addr: addr, Fingerprint: pki.Fingerprint()}})
	if err := os.WriteFile(a.dataPath(peerIdentityFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.probeReach(addr); ok {
		t.Fatal("accepted mismatched node identity")
	}
}

func TestOfflinePromoteUsesAuthenticatedGuard(t *testing.T) {
	old := promoteDelayFn
	promoteDelayFn = func() time.Duration { return 0 }
	defer func() { promoteDelayFn = old }()
	dir := t.TempDir()
	pki, err := cluster.LoadOrCreatePKI(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{opts: Options{DataDir: dir}, pki: pki}
	srv := trustedReachServer(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster.ServeReach(w, cluster.ReachView{Node: "other", Role: "master", Term: 3})
	}))
	defer srv.Close()
	if err := os.MkdirAll(a.dataPath(standbyDir), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{filepath.Join(standbyDir, stateMetaFile): StandbyState{Term: 1, Key: "k"}, peersFile: []string{srv.Listener.Addr().String()}} {
		b, _ := json.Marshal(value)
		if err := os.WriteFile(a.dataPath(name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = PromoteOffline(dir, time.Now())
	if !errors.Is(err, ErrPromoteRefused) {
		t.Fatalf("offline guard: %v", err)
	}
	if _, err := os.Stat(a.dataPath(promoteFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused promote wrote request")
	}
}
