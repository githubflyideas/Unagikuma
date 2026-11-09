package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func TestBucket(t *testing.T) {
	b := New(20.0/60, 3) // 20/m, burst 3
	for i := 0; i < 3; i++ {
		if !b.Allow(t0) {
			t.Fatalf("burst %d", i)
		}
	}
	if b.Allow(t0) {
		t.Fatal("over burst")
	}
	if d := b.Delay(t0); d != 3*time.Second {
		t.Fatalf("delay %v", d)
	}
	if !b.Allow(t0.Add(3 * time.Second)) {
		t.Fatal("refilled")
	}
	if b.Allow(t0.Add(3 * time.Second)) {
		t.Fatal("only one token refilled")
	}
	// Long idle never exceeds the burst.
	n := 0
	for b.Allow(t0.Add(time.Hour)) {
		n++
	}
	if n != 3 {
		t.Fatalf("burst cap %d", n)
	}
	// Clock going backwards does not mint tokens.
	if b.Allow(t0) {
		t.Fatal("backwards clock")
	}
}

func TestUnlimited(t *testing.T) {
	b := New(0, 1)
	for i := 0; i < 1000; i++ {
		if !b.Allow(t0) {
			t.Fatal("unlimited")
		}
	}
	if b.Delay(t0) != 0 {
		t.Fatal("delay")
	}
}

func TestSetRate(t *testing.T) {
	b := New(1, 10)
	b.SetRate(1, 2)
	n := 0
	for b.Allow(t0) {
		n++
	}
	if n != 2 {
		t.Fatalf("burst shrink: %d", n)
	}
}

func TestKeyed(t *testing.T) {
	k := NewKeyed(1, 1, 3)
	if ok, _ := k.Allow("a", t0); !ok {
		t.Fatal("a")
	}
	ok, wait := k.Allow("a", t0)
	if ok || wait != time.Second {
		t.Fatalf("a twice: %v %v", ok, wait)
	}
	if ok, _ := k.Allow("b", t0); !ok {
		t.Fatal("b independent")
	}
	// Flood of distinct keys stays bounded.
	for i := 0; i < 100; i++ {
		k.Allow(fmt.Sprint("ip", i), t0.Add(time.Duration(i)*time.Millisecond))
	}
	if k.Len() > 3 {
		t.Fatalf("unbounded: %d", k.Len())
	}
	k.SetRate(10, 5)
}

func TestKeyedConcurrent(t *testing.T) {
	k := NewKeyed(1000, 1000, 0)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				k.Allow(fmt.Sprint(g%3), t0.Add(time.Duration(i)*time.Millisecond))
			}
		}(g)
	}
	wg.Wait()
}
