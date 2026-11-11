package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

func standbyCopy(t *testing.T, a *App, term uint64) {
	t.Helper()
	if err := os.MkdirAll(a.dataPath(standbyDir), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(StandbyState{Term: term, Key: "k", Fetched: time.Now()})
	if err := os.WriteFile(a.dataPath(standbyDir, stateMetaFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.dataPath(standbyDir, stateFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A member that was master at term 1 and handed the role back at term 2
// keeps an old copy of the state. Its promote must still go past term 2,
// or the old master would win when it returns and undo the take-over.
func TestPromoteAfterHandoverRoundTripTakesANewerTerm(t *testing.T) {
	prev := promoteDelayFn
	promoteDelayFn = func() time.Duration { return 0 }
	defer func() { promoteDelayFn = prev }()

	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	standbyCopy(t, a, 0)
	if err := os.WriteFile(a.dataPath(termFile), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Superseded{Term: 2, At: time.Now(), By: "first"})
	if err := os.WriteFile(a.dataPath(supersededFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	term, err := a.Promote(context.Background(), 0, "tester")
	if err != nil || term != 3 {
		t.Fatalf("promote: term=%d err=%v, want term 3", term, err)
	}
}

// Two members promoted at the same moment: each sees the other waiting.
// The smaller name goes ahead, the other refuses, so there is never a
// second master to settle afterwards.
func TestConcurrentPromotesLeaveOneMaster(t *testing.T) {
	prev := promoteDelayFn
	promoteDelayFn = func() time.Duration { return 0 }
	defer func() { promoteDelayFn = prev }()

	for _, tc := range []struct {
		peer    string
		term    uint64
		refused bool
	}{
		{"0-first", 2, true},  // same term, smaller name: the peer wins
		{"zz-last", 2, false}, // same term, larger name: this node wins
		{"zz-last", 3, true},  // higher term wins whatever the name
		{"0-first", 1, false}, // lower term loses whatever the name
	} {
		a, _, _, _ := newApp(t, testConfig)
		standbyCopy(t, a, 1)
		view := cluster.ReachView{Node: tc.peer, Role: "replica", Promoting: tc.term}
		srv := trustedReachServer(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cluster.ServeReach(w, view)
		}))
		_ = os.WriteFile(a.dataPath(peersFile), mustJSON([]string{srv.Listener.Addr().String()}), 0o644)

		term, err := a.Promote(context.Background(), 0, "tester")
		if tc.refused {
			var refused *PromoteRefused
			if !errors.As(err, &refused) || refused.Peer != tc.peer {
				t.Errorf("%s promoting at %d: got term=%d err=%v, want refused", tc.peer, tc.term, term, err)
			}
			if a.promoting.Load() != 0 {
				t.Errorf("%s at %d: a refused promote is still shown to peers", tc.peer, tc.term)
			}
		} else if err != nil || term != 2 {
			t.Errorf("%s promoting at %d: got term=%d err=%v, want term 2", tc.peer, tc.term, term, err)
		}
		srv.Close()
		a.Store().Close()
	}
}

func TestReachShowsAPromoteUnderWay(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	if v := a.FullReachView(); v.Promoting != 0 {
		t.Fatalf("idle node shows promoting=%d", v.Promoting)
	}
	a.promoting.Store(4)
	if v := a.FullReachView(); v.Promoting != 4 {
		t.Fatalf("promoting=%d, want 4", v.Promoting)
	}
}
