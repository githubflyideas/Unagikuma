package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Pings and weak UDP checks of one address (watched by several teams, or
// a ping next to a few UDP ports) start probe.same-target-gap apart, so
// the target's limit on ICMP replies does not fake loss. Other checks of
// that address, and checks of other addresses, start at once.
func TestSameTargetSpacing(t *testing.T) {
	var mu sync.Mutex
	starts := map[string]time.Time{}
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			mu.Lock()
			if _, seen := starts[s.Name]; !seen {
				starts[s.Name] = time.Now()
			}
			mu.Unlock()
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK}
		},
		Send:      func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink:      newMemSink(),
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "probe.same-target-gap": "300ms"})
	if err != nil {
		t.Fatal(err)
	}
	mon := func(name, typ, target, addr, depth string, ping bool) MonitorSpec {
		return MonitorSpec{Name: name, Title: name, Interval: time.Hour, Attempts: 1, RecoverAttempts: 1, DefHash: name,
			Address: addr, Ping: ping, Depth: depth, Probe: probe.Spec{Name: name, Type: typ, Target: target, Timeout: time.Second}}
	}
	var mons []MonitorSpec
	for i := range 3 {
		mons = append(mons, mon(fmt.Sprintf("ping%d", i), "PING", "10.0.0.5", "10.0.0.5", "L2", true))
	}
	mons = append(mons,
		mon("syslog", "UDP", "10.0.0.5:514", "10.0.0.5", "L1", false),
		mon("web", "TCP", "10.0.0.5:443", "10.0.0.5", "L2", false),
		mon("other", "PING", "10.0.0.6", "10.0.0.6", "L2", true))
	e.Apply(Config{Tunables: ts, Monitors: mons, Location: time.UTC})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitCond(t, "every monitor to be checked", 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(starts) == len(mons)
	})
	mu.Lock()
	defer mu.Unlock()
	var same []time.Time
	for _, n := range []string{"ping0", "ping1", "ping2", "syslog"} {
		same = append(same, starts[n])
	}
	sort.Slice(same, func(i, j int) bool { return same[i].Before(same[j]) })
	for i := 1; i < len(same); i++ {
		if d := same[i].Sub(same[i-1]); d < 250*time.Millisecond {
			t.Errorf("checks of one address started %v apart", d)
		}
	}
	first := same[0]
	for _, n := range []string{"web", "other"} {
		if d := starts[n].Sub(first); d > 200*time.Millisecond || -d > 200*time.Millisecond {
			t.Errorf("%s waited %v for the pings of another check", n, d)
		}
	}
}
