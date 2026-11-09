package engine

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// fakeClock produces samples; tests advance its parts independently.
type fakeClock struct {
	at   time.Time
	host hostclock.Reading
}

func (c *fakeClock) advance(wall, awake, total time.Duration) ClockSample {
	c.at = c.at.Add(wall)
	c.host.Awake += awake
	c.host.Total += total
	return ClockSample{At: c.at, Host: c.host, HostOK: true}
}

func newWatchdog(t *testing.T) (*Watchdog, *fakeClock, SuspendConfig) {
	t.Helper()
	cfg := SuspendConfigFrom(tunables.Set{})
	w := NewWatchdog(cfg)
	c := &fakeClock{at: t0, host: hostclock.Reading{Awake: time.Hour, Total: time.Hour}}
	w.Tick(ClockSample{At: c.at, Host: c.host, HostOK: true})
	return w, c, cfg
}

func TestWatchdogQuiet(t *testing.T) {
	w, c, _ := newWatchdog(t)
	for i := 0; i < 100; i++ {
		if in, j := w.Tick(c.advance(time.Second, time.Second, time.Second)); in != nil || j != nil {
			t.Fatalf("tick %d: %+v %+v", i, in, j)
		}
	}
	if w.InGrace(c.at) {
		t.Fatal("grace without resume")
	}
}

func TestWatchdogSuspend(t *testing.T) {
	w, c, cfg := newWatchdog(t)
	start := c.at
	// Lid closed for 10 minutes: awake clock advanced one tick, total 10m+1s.
	in, j := w.Tick(c.advance(10*time.Minute+time.Second, time.Second, 10*time.Minute+time.Second))
	if in == nil || in.Kind != InterruptSuspend || in.Duration != 10*time.Minute || !in.Notify {
		t.Fatalf("suspend: %+v", in)
	}
	if j != nil {
		t.Fatalf("a suspend is not a clock jump: %+v", j)
	}
	// A round that started before the lid closed and ended after it.
	if got, ok := w.Overlaps(start.Add(-2*time.Second), c.at.Add(time.Second)); !ok || got.Kind != InterruptSuspend {
		t.Fatal("round spanning the suspend must be flagged")
	}
	// Rounds entirely after the resume are fine.
	if _, ok := w.Overlaps(c.at.Add(time.Second), c.at.Add(3*time.Second)); ok {
		t.Fatal("later round flagged")
	}
	if !w.InGrace(c.at.Add(cfg.ResumeGrace-time.Second)) || w.InGrace(c.at.Add(cfg.ResumeGrace)) {
		t.Fatal("resume grace window")
	}
}

func TestWatchdogShortSuspendNotNotified(t *testing.T) {
	w, c, _ := newWatchdog(t)
	in, _ := w.Tick(c.advance(31*time.Second, time.Second, 31*time.Second))
	if in == nil || in.Kind != InterruptSuspend || in.Notify {
		t.Fatalf("short suspend: %+v", in)
	}
	// Below the threshold is ignored.
	if in, _ := w.Tick(c.advance(6*time.Second, time.Second, 6*time.Second)); in != nil {
		t.Fatalf("below threshold: %+v", in)
	}
}

func TestWatchdogStall(t *testing.T) {
	w, c, _ := newWatchdog(t)
	in, j := w.Tick(c.advance(5*time.Second, 5*time.Second, 5*time.Second))
	if in == nil || in.Kind != InterruptStall || in.Duration != 4*time.Second || in.Notify {
		t.Fatalf("stall: %+v", in)
	}
	if j != nil || w.InGrace(c.at) {
		t.Fatal("a stall is neither a jump nor a resume")
	}
	if in, _ := w.Tick(c.advance(3*time.Second, 3*time.Second, 3*time.Second)); in != nil {
		t.Fatalf("2s late is below the stall threshold: %+v", in)
	}
}

func TestWatchdogWallClockJump(t *testing.T) {
	w, c, _ := newWatchdog(t)
	in, j := w.Tick(c.advance(time.Hour+time.Second, time.Second, time.Second))
	if in != nil {
		t.Fatalf("wall jump must not interrupt: %+v", in)
	}
	if j == nil || j.Delta != time.Hour {
		t.Fatalf("jump: %+v", j)
	}
	_, j = w.Tick(c.advance(-time.Hour+time.Second, time.Second, time.Second))
	if j == nil || j.Delta != -time.Hour {
		t.Fatalf("backward jump: %+v", j)
	}
}

func TestWatchdogWithoutHostClock(t *testing.T) {
	cfg := SuspendConfigFrom(tunables.Set{})
	w := NewWatchdog(cfg)
	w.Tick(ClockSample{At: t0})
	in, _ := w.Tick(ClockSample{At: t0.Add(2 * time.Minute)})
	if in == nil || in.Kind != InterruptStall {
		t.Fatalf("fallback detects a gap as a stall: %+v", in)
	}
	// Mixing a host reading with a fallback one must not invent a suspend.
	in, _ = w.Tick(ClockSample{At: t0.Add(2*time.Minute + time.Second), Host: hostclock.Reading{Awake: 1, Total: time.Hour}, HostOK: true})
	if in != nil {
		t.Fatalf("mixed samples: %+v", in)
	}
}

func TestWatchdogHistoryBounded(t *testing.T) {
	w, c, _ := newWatchdog(t)
	for i := 0; i < 3*maxInterruptions; i++ {
		w.Tick(c.advance(5*time.Second, 5*time.Second, 5*time.Second))
	}
	if len(w.recent) != maxInterruptions {
		t.Fatalf("history %d", len(w.recent))
	}
}

func TestOverload(t *testing.T) {
	w, _, cfg := newWatchdog(t)
	at := t0
	// Too few rounds: never judged.
	for i := 0; i < cfg.OverloadMinRounds-1; i++ {
		w.RecordRound(at, true)
	}
	if ev := w.EvaluateOverload(at); ev != nil {
		t.Fatalf("below min rounds: %+v", ev)
	}
	// 20 dropped of 100 rounds = 20%.
	w.RecordRound(at, true)
	for i := 0; i < 80; i++ {
		w.RecordRound(at.Add(time.Minute), false)
	}
	ev := w.EvaluateOverload(at.Add(time.Minute))
	if ev == nil || !ev.Enter || ev.Rounds != 100 {
		t.Fatalf("overload: %+v", ev)
	}
	if ev := w.EvaluateOverload(at.Add(time.Minute)); ev != nil {
		t.Fatalf("no repeat while overloaded: %+v", ev)
	}
	// The bad minute leaves the window.
	later := at.Add(cfg.OverloadWindow + time.Minute)
	for i := 0; i < 50; i++ {
		w.RecordRound(later, false)
	}
	ev = w.EvaluateOverload(later)
	if ev == nil || ev.Enter {
		t.Fatalf("overload exit: %+v", ev)
	}
}

func TestOverloadWindowChangeResets(t *testing.T) {
	w, _, cfg := newWatchdog(t)
	for i := 0; i < 100; i++ {
		w.RecordRound(t0, true)
	}
	cfg.OverloadWindow = 30 * time.Minute
	w.SetConfig(cfg)
	if ev := w.EvaluateOverload(t0); ev != nil {
		t.Fatalf("stats must reset: %+v", ev)
	}
}

// Rounds late because the process was stopped (SIGSTOP, a paused VM) are
// not an overloaded host: the stall clears what was counted around it.
func TestStallIsNotOverload(t *testing.T) {
	w, c, cfg := newWatchdog(t)
	for i := 0; i < cfg.OverloadMinRounds*2; i++ {
		w.RecordRound(c.at, true)
	}
	if in, _ := w.Tick(c.advance(90*time.Second, 90*time.Second, 90*time.Second)); in == nil {
		t.Fatal("no stall")
	}
	if ev := w.EvaluateOverload(c.at); ev != nil {
		t.Fatalf("overload after a stall: %+v", ev)
	}
}
