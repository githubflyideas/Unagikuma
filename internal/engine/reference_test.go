package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// A small setup: two web sites in two networks, nothing else on the
// internet to compare them with but the reference points, one of which
// (8.8.8.8) is blocked from here.
//
//   - Both sites go down while the internet works: both are alerted at once,
//     not held back as if this host had lost its network.
//   - This host loses its uplink: the outage is recognised from the two
//     sites and the reference points together, and nothing is alerted.
func TestTwoSitesAndReferencePoints(t *testing.T) {
	t.Run("both sites down", func(t *testing.T) { twoSites(t, false) })
	t.Run("this host's uplink down", func(t *testing.T) { twoSites(t, true) })
}

func twoSites(t *testing.T, uplinkDown bool) {
	var down atomic.Bool
	var mu sync.Mutex
	var sent []string
	var asked atomic.Int32
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			ref := s.Name == "gate" && !strings.HasPrefix(s.Target, "198.51.")
			if ref {
				asked.Add(1)
				if strings.HasPrefix(s.Target, "8.8.8.8:") {
					// Blocked from here: it never answers, and only after
					// the timeout.
					time.Sleep(s.Timeout)
					return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout, Detail: "no response within the timeout"}
				}
			}
			if down.Load() && (!ref || uplinkDown) {
				return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout, Detail: "no response within the timeout"}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Evidence: &probe.Evidence{Peer: s.Target}}
		},
		Send: func(_ context.Context, ch notify.Channel, m *notify.Message) notify.Attempt {
			mu.Lock()
			sent = append(sent, m.Kind+" "+m.Title)
			mu.Unlock()
			if uplinkDown && down.Load() {
				return notify.Attempt{Error: "network is unreachable"}
			}
			return notify.Attempt{OK: true}
		},
		Sink:      newMemSink(),
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.window": "10s", "notify.retry-backoff": "200ms", "localnet.notify-after": "0s"})
	if err != nil {
		t.Fatal(err)
	}
	var mons []MonitorSpec
	for i := range 2 {
		n := fmt.Sprintf("site%d", i)
		mons = append(mons, MonitorSpec{Name: n, Title: n, Interval: 200 * time.Millisecond, Retry: 200 * time.Millisecond, Attempts: 2, RecoverAttempts: 1, DefHash: n,
			Probe: probe.Spec{Name: n, Type: "TCP", Target: fmt.Sprintf("198.51.%d.10:443", 100+i), Timeout: 50 * time.Millisecond}})
	}
	hook := ChannelSpec{Channel: notify.Channel{Name: "hook", Kind: "webhook"}, Scope: netscope.WAN, Family: FamilyV4}
	e.Apply(Config{Tunables: ts, Monitors: mons, LocalDetect: true, Location: time.UTC,
		Channels: map[string]ChannelSpec{"hook": hook},
		Policies: map[string]PolicySpec{"default": {Name: "default", Chain: []string{"hook"}, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	waitCond(t, "both sites to be OK first", 20*time.Second, func() bool {
		for _, r := range e.Rows() {
			if r.Status != StatusOK {
				return false
			}
		}
		return true
	})
	down.Store(true)

	if !uplinkDown {
		waitCond(t, "both sites to be alerted", 10*time.Second, func() bool {
			mu.Lock()
			defer mu.Unlock()
			n := 0
			for _, m := range sent {
				if strings.HasPrefix(m, "PROBLEM") {
					n++
				}
			}
			return n == 2
		})
		if asked.Load() == 0 {
			t.Fatal("the reference points were never asked")
		}
		// The sites stay down: the blocked reference point must not turn
		// that into an outage of this host's own.
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if h := e.Overview().Health; h.Uplink != "ok" {
				t.Fatalf("the internet works, yet: %+v", h)
			}
		}
		return
	}

	waitCond(t, "the uplink outage to be recognised", 20*time.Second, func() bool {
		return e.Overview().Health.Uplink == "wan_down"
	})
	time.Sleep(time.Second)
	mu.Lock()
	defer mu.Unlock()
	for _, m := range sent {
		if strings.HasPrefix(m, "PROBLEM") {
			t.Fatalf("a site was alerted although this host lost its network: %v", sent)
		}
	}
}
