package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

type depEnv struct {
	t    *testing.T
	e    *Engine
	sink *memSink
	mu   sync.Mutex
	down map[string]bool
}

func newDepEnv(t *testing.T, maints ...MaintSpec) *depEnv {
	t.Helper()
	d := &depEnv{t: t, sink: newMemSink(), down: map[string]bool{}}
	d.e = New(Deps{
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.down[s.Name] {
				return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Evidence: &probe.Evidence{}}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Evidence: &probe.Evidence{}}
		},
		Send: func(context.Context, notify.Channel, *notify.Message) notify.Attempt { return notify.Attempt{OK: true} },
		Sink: d.sink, HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{
		"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.enabled": "off", "engine.dependency-wait": "300ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	mon := func(name string, parents ...string) MonitorSpec {
		return MonitorSpec{Name: name, Title: name, Probe: probe.Spec{Name: name, Type: "TCP", Target: "10.0.0.1:80", Timeout: 100 * time.Millisecond},
			Interval: 50 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DefHash: name, Parents: parents}
	}
	d.e.Apply(Config{Tunables: ts, Monitors: []MonitorSpec{mon("gw"), mon("srv", "gw")}, Maints: maints, Location: time.UTC})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return d
}

func (d *depEnv) set(name string, down bool) {
	d.mu.Lock()
	d.down[name] = down
	d.mu.Unlock()
}

func (d *depEnv) status(name string) string {
	r, _ := d.e.Row(name)
	return r.Status
}

func (d *depEnv) wait(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			d.t.Fatalf("timed out waiting for %s (gw=%s srv=%s)", what, d.status("gw"), d.status("srv"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (d *depEnv) incident(name string) bool {
	_, ok := d.sink.open(IncidentNormal, name)
	return ok
}

func TestChildOfDownParentIsUnreachable(t *testing.T) {
	d := newDepEnv(t)
	d.wait("both ok", func() bool { return d.status("gw") == "ok" && d.status("srv") == "ok" })
	d.set("gw", true)
	d.set("srv", true)
	d.wait("srv unreachable", func() bool { return d.status("srv") == StatusUnreachable && d.incident("gw") })
	time.Sleep(500 * time.Millisecond) // past dependency-wait
	if d.incident("srv") || d.status("srv") != StatusUnreachable {
		t.Fatal("a child whose parent is down must not open an incident")
	}

	// The parent comes back but the child stays down: now it is the
	// child's own failure.
	d.set("gw", false)
	d.wait("srv incident", func() bool { return d.incident("srv") && d.status("srv") == StatusCritical })
}

func TestChildRecoveringWithParentNeverNotifies(t *testing.T) {
	d := newDepEnv(t)
	d.wait("both ok", func() bool { return d.status("gw") == "ok" && d.status("srv") == "ok" })
	d.set("gw", true)
	d.set("srv", true)
	d.wait("srv unreachable", func() bool { return d.status("srv") == StatusUnreachable })
	d.set("gw", false)
	d.set("srv", false)
	d.wait("both ok again", func() bool { return d.status("gw") == "ok" && d.status("srv") == "ok" })
	time.Sleep(400 * time.Millisecond)
	if d.incident("srv") {
		t.Fatal("srv recovered with its parent; it must not have been notified")
	}
}

func TestChildFailingAloneNotifies(t *testing.T) {
	d := newDepEnv(t)
	d.wait("both ok", func() bool { return d.status("gw") == "ok" && d.status("srv") == "ok" })
	d.set("srv", true)
	d.wait("srv incident", func() bool { return d.incident("srv") })
	if d.status("srv") != StatusCritical {
		t.Fatalf("srv: %s", d.status("srv"))
	}
}
