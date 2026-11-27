package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHashSlotsBound: no more than maxHashes password hashes run at once.
func TestHashSlotsBound(t *testing.T) {
	var releases []func()
	for range maxHashes {
		releases = append(releases, hashSlot())
	}
	got := make(chan struct{})
	go func() { hashSlot()(); close(got) }()
	select {
	case <-got:
		t.Fatal("a hash started while every slot was taken")
	case <-time.After(100 * time.Millisecond):
	}
	releases[0]()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("a freed slot was not handed on")
	}
	for _, r := range releases[1:] {
		r()
	}
}

// TestHashSlotsBoundUnderBurst: a burst of hashes arriving together —
// failed sign-ins from many addresses at once, the case the backstop
// exists for — never runs more than maxHashes at a time, and every
// waiter is served in the end.
func TestHashSlotsBoundUnderBurst(t *testing.T) {
	const burst = 4 * maxHashes
	var inFlight, peak atomic.Int64
	var wg sync.WaitGroup
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := hashSlot()
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond) // stand in for the hash
			inFlight.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > maxHashes {
		t.Fatalf("peak concurrent hashes %d exceeds the bound %d", p, maxHashes)
	} else if p < maxHashes {
		t.Fatalf("peak concurrent hashes %d: the burst never filled the bound %d", p, maxHashes)
	}
}

// TestCheckPasswordBurstStaysBounded runs real password checks in
// parallel — more than there are slots, the shape of a flood of failed
// sign-ins from many addresses: they all complete, and they all fail.
func TestCheckPasswordBurstStaysBounded(t *testing.T) {
	hash := HashPassword("correct horse battery staple")
	var wg sync.WaitGroup
	errs := make(chan string, 2*maxHashes)
	for range 2 * maxHashes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if CheckPassword(hash, "wrong password") {
				errs <- "a wrong password verified"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("the right password was refused after the burst")
	}
}
