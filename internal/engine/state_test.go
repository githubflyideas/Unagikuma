package engine

import "testing"

func vk(k VerdictKind) Verdict { return Verdict{Kind: k} }

func run(s MonState, p StepParams, ks ...VerdictKind) (MonState, []Transition) {
	var ts []Transition
	for _, k := range ks {
		t := Step(s, vk(k), p)
		ts = append(ts, t)
		s = t.To
	}
	return s, ts
}

// Rows of the normative state transition table.
func TestStateTable(t *testing.T) {
	p := StepParams{Attempts: 3, RecoverAttempts: 1}

	// Row 1: pending → OK; PARTIAL sets the flag.
	s, ts := run(NewState(), p, VerdictPartial)
	if s.Avail != StatusOK || !s.Partial || !ts[0].PartialStarted {
		t.Fatalf("row1: %+v", s)
	}
	// Row 2: OK → UNKNOWN.
	s, ts = run(s, p, VerdictUnknown)
	if s.Avail != StatusUnknown || !ts[0].UnknownStarted || ts[0].To.Partial {
		t.Fatalf("row2: %+v", s)
	}
	// Row 3/4: SOFT 1/3, 2/3, then HARD.
	s, ts = run(NewState(), p, VerdictCritical, VerdictCritical)
	if s.Avail != StatusCritical || s.Hard || s.Attempt != 2 || !ts[1].SoftFailure {
		t.Fatalf("row3/4 soft: %+v", s)
	}
	s, ts = run(s, p, VerdictCritical)
	if !s.Hard || !ts[0].EnterHard {
		t.Fatalf("row4 hard: %+v", s)
	}
	// Row 7: HARD stays HARD without re-entering.
	s, ts = run(s, p, VerdictCritical)
	if ts[0].EnterHard || !s.Hard {
		t.Fatalf("row7: %+v", ts[0])
	}
	// Row 9: UNKNOWN holds HARD CRITICAL.
	s, ts = run(s, p, VerdictUnknown)
	if s.Avail != StatusCritical || !s.Held || !ts[0].HeldStarted {
		t.Fatalf("row9: %+v", s)
	}
	// Row 8: recovery (PARTIAL counts as OK).
	s, ts = run(s, p, VerdictPartial)
	if s.Avail != StatusOK || !ts[0].Recovered || !ts[0].HeldEnded || !s.Partial {
		t.Fatalf("row8: %+v %+v", s, ts[0])
	}

	// Row 5: SOFT cleared without alert.
	s, ts = run(NewState(), p, VerdictCritical, VerdictOK)
	if s.Avail != StatusOK || !ts[1].SoftCleared || ts[1].Recovered {
		t.Fatalf("row5: %+v", ts[1])
	}
	// Row 6: SOFT + UNKNOWN keeps attempt.
	s, _ = run(NewState(), p, VerdictCritical, VerdictUnknown)
	if s.Avail != StatusCritical || s.Hard || s.Attempt != 1 {
		t.Fatalf("row6: %+v", s)
	}
	s, ts = run(s, p, VerdictCritical, VerdictCritical)
	if !ts[1].EnterHard {
		t.Fatalf("row6 continue to HARD: %+v", s)
	}
}

func TestAttemptsOneGoesHardImmediately(t *testing.T) {
	_, ts := run(NewState(), StepParams{Attempts: 1}, VerdictCritical)
	if !ts[0].EnterHard {
		t.Fatal("attempts=1 must go HARD on first failure")
	}
}

func TestRecoverAttempts(t *testing.T) {
	p := StepParams{Attempts: 1, RecoverAttempts: 2}
	s, ts := run(NewState(), p, VerdictCritical, VerdictOK)
	if s.Avail != StatusCritical || ts[1].Recovered || s.OKStreak != 1 {
		t.Fatalf("first OK must not recover: %+v", s)
	}
	_, ts = run(s, p, VerdictOK)
	if !ts[0].Recovered {
		t.Fatal("second OK must recover")
	}
	// an intervening failure resets the streak.
	s, _ = run(NewState(), p, VerdictCritical, VerdictOK, VerdictCritical, VerdictOK)
	if s.Avail != StatusCritical {
		t.Fatalf("streak not reset: %+v", s)
	}
}

func TestDegradedDimension(t *testing.T) {
	p := StepParams{Attempts: 2, Slow: true}
	s, _ := run(NewState(), p, VerdictOK)
	if s.Degraded {
		t.Fatal("one slow round must not degrade with attempts=2")
	}
	s, ts := run(s, p, VerdictOK)
	if !s.Degraded || s.Display() != StatusDegraded || !ts[0].DegradedStarted {
		t.Fatalf("two slow rounds: %+v", s)
	}
	s, ts = run(s, StepParams{Attempts: 2}, VerdictOK)
	if s.Degraded || !ts[0].DegradedEnded {
		t.Fatalf("fast round clears: %+v", s)
	}
	s, _ = run(s, StepParams{Attempts: 2, OtherDegraded: true}, VerdictOK)
	if !s.Degraded {
		t.Fatal("cert/KoD degrade immediately")
	}
	// availability wins over performance.
	s, _ = run(s, StepParams{Attempts: 1, Slow: true}, VerdictCritical)
	if s.Display() != StatusCritical {
		t.Fatalf("display priority: %s", s.Display())
	}
}
