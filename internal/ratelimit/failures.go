package ratelimit

import (
	"sync"
	"time"
)

// Failures counts failed attempts per key (an address, an account) and
// locks a key out for a while once too many failures land inside a
// window. It complements the token buckets: a bucket prices every
// request the same, while this prices only failures — a success clears
// the key's count, and a locked key is refused before the caller does
// any of the expensive work a failure would pay for (checking a
// password costs far more than refusing it).
//
// The map is bounded like Keyed's: entries whose window has passed and
// whose lock has expired are forgotten once the map grows past maxKeys.
// A running lock is never dropped to make room: if the map is full of
// live entries, a new key goes untracked until some go stale — memory
// stays bounded without weakening a lockout already in force.
type Failures struct {
	mu       sync.Mutex
	maxFails int
	window   time.Duration
	lockFor  time.Duration
	maxKeys  int
	m        map[string]*failState
}

type failState struct {
	first time.Time // start of the current failure window
	n     int       // failures inside the window
	lock  time.Time // locked until (zero: not locked)
}

// NewFailures creates a failure limiter: maxFails failures within
// window lock the key for lockFor. maxKeys <= 0 means 10000.
func NewFailures(maxFails int, window, lockFor time.Duration, maxKeys int) *Failures {
	if maxKeys <= 0 {
		maxKeys = 10000
	}
	return &Failures{maxFails: maxFails, window: window, lockFor: lockFor, maxKeys: maxKeys,
		m: map[string]*failState{}}
}

// Locked reports whether key is locked out at now, and for how much
// longer. It records nothing.
func (f *Failures) Locked(key string, now time.Time) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.m[key]
	if st == nil || st.lock.IsZero() || !now.Before(st.lock) {
		return 0, false
	}
	return st.lock.Sub(now), true
}

// Fail records one failed attempt by key at now. Reaching maxFails
// inside the window starts the lockout and restarts the count, so the
// key begins clean when the lock ends. Attempts during a lockout are
// not counted: the lock never extends itself.
func (f *Failures) Fail(key string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.m[key]
	if st != nil && !st.lock.IsZero() && now.Before(st.lock) {
		return
	}
	if st == nil {
		if len(f.m) >= f.maxKeys {
			f.evict(now)
		}
		if len(f.m) >= f.maxKeys {
			return // full of live entries: untracked, see the type comment
		}
		st = &failState{first: now}
		f.m[key] = st
	} else if now.Sub(st.first) > f.window {
		st.n, st.first = 0, now
	}
	st.n++
	if st.n >= f.maxFails {
		st.lock = now.Add(f.lockFor)
		st.n, st.first = 0, now
	}
}

// OK forgets key's failures (a success needs no memory of them).
func (f *Failures) OK(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
}

// evict drops entries that no longer change anything (window passed,
// lock expired). Running locks and fresh counts stay.
func (f *Failures) evict(now time.Time) {
	for key, st := range f.m {
		locked := !st.lock.IsZero() && now.Before(st.lock)
		if !locked && now.Sub(st.first) > f.window {
			delete(f.m, key)
		}
	}
}
