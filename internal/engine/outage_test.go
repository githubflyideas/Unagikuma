package engine

import (
	"context"
	"fmt"
	"slices"
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

// This host loses its internet uplink for a while. The monitors fail
// before the outage is recognised (short interval, one attempt), and
// nothing can be sent while it lasts. When it is back, the people on call
// get one message saying what happened — not the queued problems, not a
// digest of them, and no recoveries for problems they never received.
var downReason = probe.ReasonHostUnreachable

func TestLocalOutageSendsOneSummary(t *testing.T) {
	t.Run("long enough to notify", func(t *testing.T) { localOutage(t, "0s", 1, false) })
	t.Run("short outage stays quiet", func(t *testing.T) { localOutage(t, "5m", 0, false) })
	// Down from the start, nothing to compare with and no exit to ask:
	// the monitors fail and try to notify before the outage is recognised.
	// Recognising it takes those failures back.
	t.Run("failures before it was recognised", func(t *testing.T) { localOutage(t, "0s", 1, true) })
	// A service already down before the outage was told once; it is not
	// told again, and gets no recovery it did not have.
	t.Run("a service down before it", func(t *testing.T) {
		outage(t, outageCase{notifyAfter: "0s", wantSystem: 1, deadBefore: "site8", want: []string{"PROBLEM site8"}})
	})
	// A service that broke while nothing could be checked is alerted once
	// the network is back, and nothing else is.
	t.Run("a service breaking during it", func(t *testing.T) {
		outage(t, outageCase{notifyAfter: "0s", wantSystem: 1, failsDuring: "site7", want: []string{"PROBLEM site7"}})
	})
}

type outageCase struct {
	notifyAfter             string
	wantSystem              int
	downAtStart             bool
	deadBefore, failsDuring string   // a monitor failing on its own (HTTP 500) from the start, or from the cut on
	want                    []string // the monitor notices expected (kind and name)
}

func localOutage(t *testing.T, notifyAfter string, wantSystem int, downAtStart bool) {
	outage(t, outageCase{notifyAfter: notifyAfter, wantSystem: wantSystem, downAtStart: downAtStart})
}

func outage(t *testing.T, c outageCase) {
	notifyAfter, wantSystem, downAtStart := c.notifyAfter, c.wantSystem, c.downAtStart
	if testing.Short() {
		t.Skip("runs through a real outage window")
	}
	var down, failing atomic.Bool
	// One target keeps answering until the others have failed, so the
	// self-check finds the network working until then.
	var lastStanding atomic.Bool
	lastStanding.Store(downAtStart)
	sink := &traceSink{memSink: newMemSink()}
	var mu sync.Mutex
	var got, tried []string
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			if s.Name == c.deadBefore || s.Name == c.failsDuring && failing.Load() {
				if !down.Load() {
					return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonHTTPStatus, Detail: "HTTP 500", Evidence: &probe.Evidence{Peer: s.Target}}
				}
			}
			if down.Load() && !(lastStanding.Load() && strings.HasPrefix(s.Target, "198.51.108.")) {
				return probe.Result{Vote: probe.VoteFail, Reason: downReason, Detail: "connect: network is unreachable"}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Evidence: &probe.Evidence{Peer: s.Target}}
		},
		Send: func(_ context.Context, ch notify.Channel, m *notify.Message) notify.Attempt {
			if down.Load() {
				mu.Lock()
				tried = append(tried, m.Kind+" "+m.Title)
				mu.Unlock()
				return notify.Attempt{Error: "network is unreachable"}
			}
			mu.Lock()
			got = append(got, m.Kind+" "+m.Title)
			mu.Unlock()
			return notify.Attempt{OK: true}
		},
		Sink:      sink,
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	gateHosts := "5"
	if downAtStart {
		gateHosts = "50" // the target still answering is among the ones re-checked
	}
	ts, err := tunables.New(map[string]string{"localnet.gate-hosts": gateHosts, "engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.window": "10s", "notify.retry-backoff": "200ms", "localnet.notify-after": notifyAfter, "localnet.exit-target": ""})
	if err != nil {
		t.Fatal(err)
	}
	var mons []MonitorSpec
	for i := range 9 {
		n := fmt.Sprintf("site%d", i)
		mons = append(mons, MonitorSpec{Name: n, Title: n, Interval: 200 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DefHash: n,
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

	// All fine first: every target must have answered once, or a target
	// never seen answering is no evidence of a cut (it may be dead).
	waitCond(t, "every monitor to be OK first", 20*time.Second, func() bool {
		for _, r := range e.Rows() {
			if r.Status != StatusOK && r.Name != c.deadBefore {
				return false
			}
		}
		return true
	})
	if c.deadBefore != "" {
		waitCond(t, "the dead service to be told", 20*time.Second, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return slices.ContainsFunc(got, func(m string) bool { return strings.HasPrefix(m, "PROBLEM "+c.deadBefore) })
		})
	}
	down.Store(true)
	failing.Store(true)
	if downAtStart {
		// One target still answers for a moment: the self-check rightly
		// finds the network working, and the others turn CRITICAL before
		// the outage can be recognised.
		waitCond(t, "a monitor to fail before the outage is recognised", 20*time.Second, func() bool {
			r, _ := e.Row("site0")
			return r.Status == StatusCritical
		})
		lastStanding.Store(false)
	}
	for deadline := time.Now().Add(45 * time.Second); e.Overview().Health.Uplink != "wan_down"; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			var st []string
			for _, r := range e.Rows() {
				st = append(st, r.Name+"="+r.Status+"/"+r.Reason)
			}
			var ln string
			e.do(func() {
				for f, fs := range e.localnet.fam {
					ln += fmt.Sprintf("fam %d episode %v lastReachWAN %v preDead %v;", f, fs.episodeStart.Format("15:04:05.000"), fs.lastReachWAN.Format("15:04:05.000"), fs.preDead)
					for k, d := range fs.domains {
						ln += fmt.Sprintf(" %s{at %s reach %v nf %v lastOK %s}", k, d.lastAt.Format("15:04:05.000"), d.reachable, d.networkFail, d.lastReachable.Format("15:04:05.000"))
					}
				}
			})
			t.Fatalf("timed out waiting for the outage to be recognised: health %+v, monitors %v, localnet %s", e.Overview().Health, st, ln)
		}
	}
	time.Sleep(2 * time.Second)
	down.Store(false)
	waitCond(t, "every monitor to be OK again", 10*time.Second, func() bool {
		for _, r := range e.Rows() {
			if r.Status != StatusOK && r.Name != c.deadBefore && r.Name != c.failsDuring {
				return false
			}
		}
		return true
	})
	waitCond(t, "the outage to end", 30*time.Second, func() bool { return e.Overview().Health.Uplink == "ok" })
	time.Sleep(3 * time.Second) // anything still queued goes out now

	mu.Lock()
	defer mu.Unlock()
	var monitorMsgs, system []string
	for _, m := range got {
		if strings.HasPrefix(m, "SYSTEM") {
			system = append(system, m)
		} else {
			monitorMsgs = append(monitorMsgs, m)
		}
	}
	var unexpected []string
	for _, m := range monitorMsgs {
		if !slices.ContainsFunc(c.want, func(w string) bool { return strings.HasPrefix(m, w) }) {
			unexpected = append(unexpected, m)
		}
	}
	if len(unexpected) > 0 {
		t.Errorf("monitor notifications about a local outage: %q", unexpected)
	}
	for _, w := range c.want {
		if n := len(slices.DeleteFunc(slices.Clone(monitorMsgs), func(m string) bool { return !strings.HasPrefix(m, w) })); n != 1 {
			t.Errorf("want one %q, got %d in %q", w, n, monitorMsgs)
		}
	}
	if len(system) != wantSystem {
		t.Errorf("want %d message(s) about the outage, got %q", wantSystem, system)
	}
	if t.Failed() {
		t.Logf("tried while down: %q", tried)
		sink.tmu.Lock()
		t.Logf("intents: %q", sink.trace)
		sink.tmu.Unlock()
		sink.mu.Lock()
		for _, ev := range sink.events {
			if ev.Type == EventSystem || ev.Type == EventNotify || ev.Type == EventIncident {
				t.Logf("event %s %s %v", ev.Type, ev.Message, ev.Data["msg"])
			}
		}
		sink.mu.Unlock()
	}
}

func waitCond(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type traceSink struct {
	*memSink
	tmu   sync.Mutex
	trace []string
}

func (s *traceSink) SaveIntent(in *Intent) error {
	s.tmu.Lock()
	s.trace = append(s.trace, fmt.Sprintf("%s/%s/%s/%d", in.Kind, in.Message.Title, in.State, len(in.Delivery.Steps)))
	s.tmu.Unlock()
	return nil
}

// The uplink stays down past localnet.max-hold: the monitors are judged
// again (fail open) and their problem notices carry the "may be wrong"
// note; one system notice says the hold was released.
func TestMaxHoldReleasesStuckHold(t *testing.T) {
	if testing.Short() {
		t.Skip("runs through a real outage window")
	}
	var down atomic.Bool
	sink := &traceSink{memSink: newMemSink()}
	var mu sync.Mutex
	var tried []string
	e := New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			if down.Load() {
				return probe.Result{Vote: probe.VoteFail, Reason: downReason, Detail: "connect: network is unreachable"}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Evidence: &probe.Evidence{Peer: s.Target}}
		},
		Send: func(_ context.Context, ch notify.Channel, m *notify.Message) notify.Attempt {
			mu.Lock()
			tried = append(tried, m.Kind+" "+m.Title+" | "+m.Body)
			mu.Unlock()
			if down.Load() {
				return notify.Attempt{Error: "network is unreachable"}
			}
			return notify.Attempt{OK: true}
		},
		Sink:      sink,
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.window": "10s", "notify.retry-backoff": "200ms", "localnet.notify-after": "0s",
		"localnet.exit-target": "", "localnet.max-hold": "4s", "localnet.recheck-spread": "0s"})
	if err != nil {
		t.Fatal(err)
	}
	var mons []MonitorSpec
	for i := range 5 {
		n := fmt.Sprintf("site%d", i)
		mons = append(mons, MonitorSpec{Name: n, Title: n, Interval: 200 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DefHash: n,
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

	time.Sleep(time.Second)
	down.Store(true)
	for deadline := time.Now().Add(45 * time.Second); e.Overview().Health.Uplink != "wan_down"; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			var st []string
			for _, r := range e.Rows() {
				st = append(st, r.Name+"="+r.Status+"/"+r.Reason)
			}
			var ln string
			e.do(func() {
				for f, fs := range e.localnet.fam {
					ln += fmt.Sprintf("fam %d episode %v lastReachWAN %v preDead %v;", f, fs.episodeStart.Format("15:04:05.000"), fs.lastReachWAN.Format("15:04:05.000"), fs.preDead)
					for k, d := range fs.domains {
						ln += fmt.Sprintf(" %s{at %s reach %v nf %v lastOK %s}", k, d.lastAt.Format("15:04:05.000"), d.reachable, d.networkFail, d.lastReachable.Format("15:04:05.000"))
					}
				}
			})
			t.Fatalf("timed out waiting for the outage to be recognised: health %+v, monitors %v, localnet %s", e.Overview().Health, st, ln)
		}
	}
	waitCond(t, "the hold to be released and a monitor to fail", 20*time.Second, func() bool {
		r, _ := e.Row("site0")
		return r.Status == StatusCritical
	})
	waitCond(t, "a problem notice with the note", 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range tried {
			if strings.HasPrefix(m, "PROBLEM") && strings.Contains(m, "may be wrong") {
				return true
			}
		}
		return false
	})
	sink.mu.Lock()
	released := 0
	for _, ev := range sink.events {
		if ev.Data["msg"] == "hold_released" {
			released++
		}
	}
	sink.mu.Unlock()
	if released != 1 {
		t.Fatalf("want one hold_released event, got %d", released)
	}
}
