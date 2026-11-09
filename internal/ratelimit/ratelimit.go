// Package ratelimit provides token buckets with explicit time, used for
// channel send rates, probe packet rates and API request limits.
package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a token bucket: it refills at Rate tokens per second up to
// Burst. The zero value allows nothing; use New. Not safe for concurrent
// use; see Keyed for a concurrent map of buckets.
type Bucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

// New returns a full bucket. A rate <= 0 means unlimited.
func New(perSecond float64, burst int) *Bucket {
	b := &Bucket{rate: perSecond, burst: float64(max(1, burst))}
	b.tokens = b.burst
	return b
}

func (b *Bucket) refill(now time.Time) {
	if b.last.IsZero() {
		b.last = now
		return
	}
	if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.tokens = min(b.burst, b.tokens+dt*b.rate)
		b.last = now
	}
}

// Allow takes one token if available.
func (b *Bucket) Allow(now time.Time) bool {
	if b.rate <= 0 {
		return true
	}
	b.refill(now)
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Delay reports how long until one token is available (0 = now).
func (b *Bucket) Delay(now time.Time) time.Duration {
	if b.rate <= 0 {
		return 0
	}
	b.refill(now)
	if b.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
}

// SetRate changes the rate and burst, keeping the current fill level.
func (b *Bucket) SetRate(perSecond float64, burst int) {
	b.rate, b.burst = perSecond, float64(max(1, burst))
	b.tokens = min(b.tokens, b.burst)
}

// Keyed is a concurrent set of buckets, one per key (token, IP, channel).
// Idle buckets are forgotten once the map exceeds maxKeys, so a flood of
// distinct keys cannot grow memory without bound.
type Keyed struct {
	mu      sync.Mutex
	rate    float64
	burst   int
	maxKeys int
	m       map[string]*Bucket
}

// NewKeyed creates a keyed limiter; maxKeys <= 0 means 10000.
func NewKeyed(perSecond float64, burst, maxKeys int) *Keyed {
	if maxKeys <= 0 {
		maxKeys = 10000
	}
	return &Keyed{rate: perSecond, burst: burst, maxKeys: maxKeys, m: map[string]*Bucket{}}
}

// Allow takes a token from key's bucket; when denied it also returns how
// long the caller should wait (for Retry-After).
func (k *Keyed) Allow(key string, now time.Time) (bool, time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	b := k.m[key]
	if b == nil {
		if len(k.m) >= k.maxKeys {
			k.evict(now)
		}
		b = New(k.rate, k.burst)
		k.m[key] = b
	}
	if b.Allow(now) {
		return true, 0
	}
	return false, b.Delay(now)
}

// SetRate updates every bucket (hot reload of a tunable).
func (k *Keyed) SetRate(perSecond float64, burst int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rate, k.burst = perSecond, burst
	for _, b := range k.m {
		b.SetRate(perSecond, burst)
	}
}

// evict drops buckets that are full again (idle long enough that keeping
// them changes nothing); if none are, it drops everything, which at worst
// grants a few extra requests.
func (k *Keyed) evict(now time.Time) {
	for key, b := range k.m {
		b.refill(now)
		if b.tokens >= b.burst {
			delete(k.m, key)
		}
	}
	if len(k.m) >= k.maxKeys {
		clear(k.m)
	}
}

// Len returns the number of tracked keys.
func (k *Keyed) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}
