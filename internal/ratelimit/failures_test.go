package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// Failures below the limit never lock; the one that reaches it locks
// for the full lockout, and the count restarts when the lock starts.
func TestFailuresLockAtLimit(t *testing.T) {
	f := NewFailures(5, time.Minute, time.Minute, 0)
	for i := 1; i <= 4; i++ {
		f.Fail("a", t0)
		if _, locked := f.Locked("a", t0); locked {
			t.Fatalf("locked after %d failures", i)
		}
	}
	f.Fail("a", t0)
	wait, locked := f.Locked("a", t0)
	if !locked || wait != time.Minute {
		t.Fatalf("after the 5th failure: locked=%v wait=%v", locked, wait)
	}
	// Other keys are untouched.
	if _, locked := f.Locked("b", t0); locked {
		t.Fatal("another key locked too")
	}
}

// A lock expires with time, is not extended by attempts made during
// it, and the key then counts from zero again.
func TestFailuresLockExpires(t *testing.T) {
	f := NewFailures(3, time.Minute, time.Minute, 0)
	for range 3 {
		f.Fail("a", t0)
	}
	half := t0.Add(30 * time.Second)
	f.Fail("a", half) // during the lock: must not extend it
	if wait, locked := f.Locked("a", half); !locked || wait != 30*time.Second {
		t.Fatalf("mid-lock: locked=%v wait=%v", locked, wait)
	}
	after := t0.Add(time.Minute + time.Second)
	if _, locked := f.Locked("a", after); locked {
		t.Fatal("still locked after the lockout")
	}
	f.Fail("a", after)
	f.Fail("a", after)
	if _, locked := f.Locked("a", after); locked {
		t.Fatal("count did not restart after the lockout")
	}
}

// A success clears the count: failures before it no longer add up.
func TestFailuresOKClears(t *testing.T) {
	f := NewFailures(3, time.Minute, time.Minute, 0)
	f.Fail("a", t0)
	f.Fail("a", t0)
	f.OK("a")
	f.Fail("a", t0)
	f.Fail("a", t0)
	if _, locked := f.Locked("a", t0); locked {
		t.Fatal("locked despite the success in between")
	}
}

// Failures older than the window fall away instead of accumulating.
func TestFailuresWindowExpires(t *testing.T) {
	f := NewFailures(3, time.Minute, time.Minute, 0)
	f.Fail("a", t0)
	f.Fail("a", t0)
	later := t0.Add(2 * time.Minute)
	f.Fail("a", later)
	f.Fail("a", later)
	if _, locked := f.Locked("a", later); locked {
		t.Fatal("failures from an old window still counted")
	}
}

// A flood of distinct keys cannot grow the map without bound.
func TestFailuresMapBounded(t *testing.T) {
	f := NewFailures(5, time.Minute, time.Minute, 100)
	for i := range 1000 {
		f.Fail(fmt.Sprintf("ip-%d", i), t0)
	}
	if len(f.m) > 100 {
		t.Fatalf("map holds %d keys, cap is 100", len(f.m))
	}
	// A running lock is never dropped to make room: with the map full
	// of live locks, a new key goes untracked instead.
	f2 := NewFailures(1, time.Minute, time.Minute, 2)
	f2.Fail("a", t0) // maxFails 1: locked at once
	f2.Fail("b", t0)
	f2.Fail("c", t0.Add(10*time.Second)) // no room, nothing stale
	if len(f2.m) != 2 {
		t.Fatalf("map holds %d keys, cap is 2", len(f2.m))
	}
	if _, locked := f2.Locked("a", t0.Add(10*time.Second)); !locked {
		t.Fatal("eviction dropped a running lock")
	}
	if _, locked := f2.Locked("c", t0.Add(10*time.Second)); locked {
		t.Fatal("the untracked key was recorded anyway")
	}
	// Once entries go stale they are forgotten and new keys count again.
	f2.Fail("c", t0.Add(2*time.Minute))
	if _, locked := f2.Locked("c", t0.Add(2*time.Minute)); !locked {
		t.Fatal("a fresh key was not tracked after stale entries expired")
	}
}
