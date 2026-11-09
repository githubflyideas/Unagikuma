package engine

import (
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Suspend and stall detection: a laptop that sleeps, a VM the hypervisor
// pauses or a process frozen by swapping must not turn into a wall of red
// when it wakes up. A watchdog ticks every SuspendConfig.Tick and compares how
// far the clocks advanced since the previous tick:
//
//   - suspend: the host clock pair says the machine was suspended for at
//     least Threshold;
//   - stall: the process was awake but our tick came late by at least Stall
//     (GC pause, swap storm, hypervisor steal);
//   - wall clock jump: the wall clock moved differently from real elapsed
//     time (NTP step, manual change). That is only an event: wall clock
//     changes never affect judgements because rounds are timed with the
//     monotonic clock.
//
// The engine asks Overlaps for every finished round (rounds spanning an
// interruption abstain and do not count as an attempt) and InGrace for
// network-class failures right after a resume (Wi-Fi and VPNs need a moment
// to come back).

// SuspendConfig holds the tunables.
type SuspendConfig struct {
	Threshold         time.Duration
	Stall             time.Duration
	Tick              time.Duration
	ResumeGrace       time.Duration
	RecheckSpread     time.Duration
	NotifyAfter       time.Duration
	NotifyRestart     bool
	OverloadRatio     float64
	OverloadWindow    time.Duration
	OverloadMinRounds int
}

// SuspendConfigFrom reads the tunables.
func SuspendConfigFrom(t tunables.Set) SuspendConfig {
	return SuspendConfig{
		Threshold:         t.Duration("suspend.threshold"),
		Stall:             t.Duration("suspend.stall"),
		Tick:              t.Duration("suspend.tick"),
		ResumeGrace:       t.Duration("suspend.resume-grace"),
		RecheckSpread:     t.Duration("suspend.recheck-spread"),
		NotifyAfter:       t.Duration("suspend.notify-after"),
		NotifyRestart:     t.Bool("suspend.notify-restart"),
		OverloadRatio:     t.Float("suspend.overload-ratio"),
		OverloadWindow:    t.Duration("suspend.overload-window"),
		OverloadMinRounds: t.Int("suspend.overload-min-rounds"),
	}
}

// ClockSample is one watchdog tick.
type ClockSample struct {
	// At is time.Now(): it carries Go's monotonic reading (used for interval
	// arithmetic) and the wall clock (used for jump detection and messages).
	At time.Time
	// Host is the suspend-aware clock pair; HostOK is false when the
	// platform has none.
	Host   hostclock.Reading
	HostOK bool
}

// Interruption kinds; they double as the abstain reasons.
const (
	InterruptSuspend = "local_suspend"
	InterruptStall   = "local_stall"
)

// Interruption is a period during which this process was not watching.
type Interruption struct {
	Kind string
	// Start and End are the ticks bracketing the interruption.
	Start, End time.Time
	// Duration is how long monitoring was interrupted.
	Duration time.Duration
	// Notify is true when Duration reaches NotifyAfter: the operator gets
	// one "monitoring interrupted for X" system notice.
	Notify bool
}

// ClockJump reports a wall clock change: the wall clock moved Delta more
// (or, when negative, less) than real time did.
type ClockJump struct {
	At    time.Time
	Delta time.Duration
}

// OverloadEvent is an enter/exit of the "host overloaded" condition.
type OverloadEvent struct {
	Enter  bool
	Ratio  float64
	Rounds int
}

// overloadBuckets is the resolution of the sliding overload window.
const overloadBuckets = 60

type roundBucket struct {
	id             int64
	total, dropped int
}

// maxInterruptions bounds the history kept for Overlaps; rounds last
// seconds to minutes, so only the latest few interruptions can matter.
const maxInterruptions = 32

// Watchdog is the suspend/stall detector. Not safe for concurrent use.
type Watchdog struct {
	cfg        SuspendConfig
	prev       ClockSample
	have       bool
	origin     time.Time
	recent     []Interruption
	lastResume time.Time // end of the last suspend
	buckets    [overloadBuckets]roundBucket
	overloaded bool
}

// NewWatchdog creates a watchdog.
func NewWatchdog(cfg SuspendConfig) *Watchdog { return &Watchdog{cfg: cfg} }

// SetConfig replaces tunables (hot reload). Changing the overload window
// resets the overload statistics.
func (w *Watchdog) SetConfig(cfg SuspendConfig) {
	if cfg.OverloadWindow != w.cfg.OverloadWindow {
		w.buckets = [overloadBuckets]roundBucket{}
	}
	w.cfg = cfg
}

// Tick feeds one sample and reports a detected interruption and/or clock
// jump since the previous sample.
func (w *Watchdog) Tick(s ClockSample) (*Interruption, *ClockJump) {
	if !w.have {
		w.prev, w.have, w.origin = s, true, s.At
		return nil, nil
	}
	prev := w.prev
	w.prev = s
	if !prev.HostOK || !s.HostOK {
		// Comparing a host reading with a fallback reading is meaningless.
		s.HostOK, prev.HostOK = false, false
	}

	mono := s.At.Sub(prev.At) // Go's monotonic clock: stops while suspended on Linux, macOS and Windows
	awake, total, suspended := mono, mono, time.Duration(0)
	if s.HostOK {
		awake = s.Host.Awake - prev.Host.Awake
		total = s.Host.Total - prev.Host.Total
		suspended = s.Host.Suspended(prev.Host)
	}

	var in *Interruption
	switch late := awake - w.cfg.Tick; {
	case suspended >= w.cfg.Threshold:
		in = &Interruption{Kind: InterruptSuspend, Duration: max(suspended, total-w.cfg.Tick)}
		w.lastResume = s.At
	case late >= w.cfg.Stall:
		in = &Interruption{Kind: InterruptStall, Duration: late}
	}
	if in != nil {
		in.Start, in.End = prev.At, s.At
		in.Notify = in.Duration >= w.cfg.NotifyAfter
		w.recent = append(w.recent, *in)
		// Rounds late because of it say nothing about the host's load.
		w.buckets = [overloadBuckets]roundBucket{}
		if len(w.recent) > maxInterruptions {
			w.recent = append(w.recent[:0], w.recent[len(w.recent)-maxInterruptions:]...)
		}
	}

	var jump *ClockJump
	if drift := s.At.Round(0).Sub(prev.At.Round(0)) - total; drift >= w.cfg.Threshold || -drift >= w.cfg.Threshold {
		jump = &ClockJump{At: s.At, Delta: drift}
	}
	return in, jump
}

// Overlaps reports the latest interruption overlapping a round that ran
// from start to end (times from time.Now, so they carry monotonic readings).
func (w *Watchdog) Overlaps(start, end time.Time) (Interruption, bool) {
	for i := len(w.recent) - 1; i >= 0; i-- {
		in := w.recent[i]
		if in.Start.Before(end) && in.End.After(start) {
			return in, true
		}
	}
	return Interruption{}, false
}

// InGrace reports whether now is within ResumeGrace after a suspend ended;
// network-class failures then abstain with InterruptSuspend.
func (w *Watchdog) InGrace(now time.Time) bool {
	return !w.lastResume.IsZero() && now.Sub(w.lastResume) < w.cfg.ResumeGrace
}

// RecordRound counts a scheduled round; dropped means it could not be
// started on time (saturated host).
func (w *Watchdog) RecordRound(at time.Time, dropped bool) {
	b := w.bucket(at)
	if b == nil {
		return
	}
	b.total++
	if dropped {
		b.dropped++
	}
}

func (w *Watchdog) bucketWidth() time.Duration {
	return max(w.cfg.OverloadWindow/overloadBuckets, time.Second)
}

func (w *Watchdog) bucket(at time.Time) *roundBucket {
	if w.origin.IsZero() {
		w.origin = at
	}
	id := int64(at.Sub(w.origin) / w.bucketWidth())
	if id < 0 {
		return nil
	}
	b := &w.buckets[id%overloadBuckets]
	if b.id != id {
		*b = roundBucket{id: id}
	}
	return b
}

// EvaluateOverload returns an event when the dropped-round ratio over the
// overload window crosses OverloadRatio (with at least OverloadMinRounds
// rounds in the window).
func (w *Watchdog) EvaluateOverload(now time.Time) *OverloadEvent {
	total, dropped := 0, 0
	if !w.origin.IsZero() {
		cur := int64(now.Sub(w.origin) / w.bucketWidth())
		for _, b := range w.buckets {
			if (b.total > 0) && b.id > cur-overloadBuckets && b.id <= cur {
				total += b.total
				dropped += b.dropped
			}
		}
	}
	ratio := 0.0
	if total > 0 {
		ratio = float64(dropped) / float64(total)
	}
	over := total >= max(1, w.cfg.OverloadMinRounds) && ratio >= w.cfg.OverloadRatio
	if over == w.overloaded {
		return nil
	}
	w.overloaded = over
	return &OverloadEvent{Enter: over, Ratio: ratio, Rounds: total}
}
