package engine

// Status values. The display status is the worse of the
// availability and performance dimensions.
const (
	StatusOK          = "ok"
	StatusDegraded    = "degraded"
	StatusCritical    = "critical"
	StatusUnknown     = "unknown"
	StatusPending     = "pending"
	StatusPaused      = "paused"
	StatusMaintenance = "maintenance"
	StatusUnreachable = "unreachable" // failing while a parent is down
)

// MonState is the persisted state machine of one monitor.
type MonState struct {
	// Avail is the availability dimension: ok, critical, unknown, pending.
	Avail         string
	Hard          bool // state type: HARD (true) or SOFT
	Attempt       int  // consecutive failing rounds (SOFT counter)
	OKStreak      int  // consecutive OK rounds while HARD critical (recover-attempts)
	Partial       bool // last verdict was PARTIAL (◔ flag)
	Held          bool // HARD critical held because the round was UNKNOWN
	UnknownReason string
	SlowStreak    int  // consecutive slow rounds (performance dimension)
	Slow          bool // performance dimension: DEGRADED by latency
	Degraded      bool // DEGRADED for any reason (latency, cert, KoD RATE, PUSH late)
}

// NewState is the PENDING state of a new or redefined monitor.
func NewState() MonState { return MonState{Avail: StatusPending, Hard: true} }

// Display returns the single status shown in the UI (priority:
// CRITICAL > UNKNOWN > DEGRADED > OK; pending overrides).
func (s MonState) Display() string {
	switch s.Avail {
	case StatusCritical, StatusUnknown, StatusPending:
		return s.Avail
	}
	if s.Degraded {
		return StatusDegraded
	}
	return StatusOK
}

// StepParams are the per-monitor knobs for one transition.
type StepParams struct {
	Attempts         int
	RecoverAttempts  int
	DegradedAttempts int  // 0 = same as Attempts
	Slow             bool // this round's median OK latency exceeded the degraded threshold
	OtherDegraded    bool // cert-days, KoD RATE, PUSH late... (degrades immediately)
}

// Transition is what happened in one step; the engine turns it into
// incidents, notifications and events.
type Transition struct {
	From, To MonState
	// EnterHard is true when the monitor just became HARD CRITICAL: open (or
	// reopen) an incident.
	EnterHard bool
	// Recovered is true when a HARD CRITICAL monitor recovered (row 8).
	Recovered bool
	// SoftCleared: a SOFT problem cleared without alerting (row 5) — counts
	// toward the "soft_filtered" value counter.
	SoftCleared bool
	// SoftFailure: a new or continued SOFT failure (attempt < attempts).
	SoftFailure bool
	// PartialStarted / PartialEnded: the ◔ flag changed.
	PartialStarted, PartialEnded bool
	// DegradedStarted / DegradedEnded: the performance dimension changed.
	DegradedStarted, DegradedEnded bool
	// UnknownStarted: entered UNKNOWN (row 2); Held: HARD held by UNKNOWN (row 9).
	UnknownStarted, HeldStarted, HeldEnded bool
}

// Changed reports whether the displayed status or state type changed.
func (t Transition) Changed() bool {
	return t.From.Display() != t.To.Display() || t.From.Hard != t.To.Hard
}

// Step applies the normative state transition table and the
// performance dimension for one round verdict.
func Step(prev MonState, v Verdict, p StepParams) Transition {
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	if p.RecoverAttempts < 1 {
		p.RecoverAttempts = 1
	}
	s := prev
	t := Transition{From: prev}
	wasHardCrit := prev.Avail == StatusCritical && prev.Hard
	wasSoftCrit := prev.Avail == StatusCritical && !prev.Hard

	switch v.Kind {
	case VerdictCritical:
		s.OKStreak = 0
		s.Partial = false
		s.UnknownReason = ""
		if wasHardCrit {
			// Row 7: stay HARD, the incident keeps running.
			if prev.Held {
				s.Held = false
				t.HeldEnded = true
			}
			break
		}
		if !wasSoftCrit {
			s.Attempt = 0
		}
		s.Attempt++
		s.Avail = StatusCritical
		if s.Attempt >= p.Attempts {
			s.Hard = true
			s.Attempt = p.Attempts
			t.EnterHard = true
		} else {
			s.Hard = false
			t.SoftFailure = true
		}
	case VerdictOK, VerdictPartial:
		s.Partial = v.Kind == VerdictPartial
		s.UnknownReason = ""
		if wasHardCrit {
			// Row 8: recover after recover-attempts consecutive OK rounds.
			s.OKStreak++
			if prev.Held {
				s.Held = false
				t.HeldEnded = true
			}
			if s.OKStreak < p.RecoverAttempts {
				break
			}
			t.Recovered = true
		} else if wasSoftCrit {
			// Row 5: a SOFT problem cleared before alerting.
			t.SoftCleared = true
		}
		s.Avail, s.Hard, s.Attempt, s.OKStreak = StatusOK, true, 0, 0
	case VerdictUnknown:
		switch {
		case wasHardCrit:
			// Row 9: hold CRITICAL, pause repeats, exclude from uptime.
			s.OKStreak = 0
			if !prev.Held {
				s.Held = true
				t.HeldStarted = true
			}
		case wasSoftCrit:
			// Row 6: keep SOFT, attempt unchanged.
		default:
			// Row 2.
			if prev.Avail != StatusUnknown {
				t.UnknownStarted = true
			}
			s.Avail, s.Hard, s.Attempt = StatusUnknown, true, 0
		}
		s.UnknownReason = v.UnknownReason
		s.Partial = false
	}

	// Performance dimension: only meaningful while available.
	if s.Avail == StatusOK {
		need := p.DegradedAttempts
		if need <= 0 {
			need = p.Attempts
		}
		if p.Slow {
			s.SlowStreak++
		} else {
			s.SlowStreak = 0
		}
		s.Slow = s.SlowStreak >= need
		s.Degraded = s.Slow || p.OtherDegraded
	} else if v.Kind != VerdictUnknown {
		s.SlowStreak, s.Slow, s.Degraded = 0, false, false
	}

	t.PartialStarted = !prev.Partial && s.Partial
	t.PartialEnded = prev.Partial && !s.Partial
	t.DegradedStarted = !prev.Degraded && s.Degraded && s.Avail == StatusOK
	t.DegradedEnded = prev.Degraded && !s.Degraded
	t.To = s
	return t
}
