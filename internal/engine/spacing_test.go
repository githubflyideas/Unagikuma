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

// The packet budget (probe.max-pps) holds back pings and UDP checks only:
// with a budget of one packet a second, forty HTTP and TCP checks still
// start at once while three pings of different addresses take seconds.
func TestPacketBudgetOnlyForPackets(t *testing.T) {
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
	ts, err := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "probe.max-pps": "1"})
	if err != nil {
		t.Fatal(err)
	}
	mon := func(name, typ, target, addr string, ping bool) MonitorSpec {
		return MonitorSpec{Name: name, Title: name, Interval: time.Hour, Attempts: 1, RecoverAttempts: 1, DefHash: name,
			Address: addr, Ping: ping, Depth: "L2", Probe: probe.Spec{Name: name, Type: typ, Target: target, Timeout: time.Second}}
	}
	var mons []MonitorSpec
	for i := range 20 {
		mons = append(mons, mon(fmt.Sprintf("web%d", i), "HTTP", fmt.Sprintf("http://10.0.1.%d/", i), fmt.Sprintf("10.0.1.%d", i), false),
			mon(fmt.Sprintf("tcp%d", i), "TCP", fmt.Sprintf("10.0.2.%d:443", i), fmt.Sprintf("10.0.2.%d", i), false))
	}
	for i := range 2 {
		mons = append(mons, mon(fmt.Sprintf("ping%d", i), "PING", fmt.Sprintf("10.0.3.%d", i), fmt.Sprintf("10.0.3.%d", i), true))
	}
	mons = append(mons, mon("ping2", "SNMP", "10.0.3.9", "10.0.3.9", false)) // counted like a ping
	t0 := time.Now()
	e.Apply(Config{Tunables: ts, Monitors: mons, Location: time.UTC})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitCond(t, "every monitor to be checked", 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(starts) == len(mons)
	})
	mu.Lock()
	defer mu.Unlock()
	var last time.Time
	for n, at := range starts {
		if n[:4] != "ping" && at.Sub(t0) > 1500*time.Millisecond {
			t.Errorf("%s waited %v for the packet budget", n, at.Sub(t0))
		}
		if n[:4] == "ping" && at.After(last) {
			last = at
		}
	}
	if last.Sub(t0) < 1500*time.Millisecond {
		t.Errorf("three pings started within %v on a budget of one packet a second", last.Sub(t0))
	}
}
