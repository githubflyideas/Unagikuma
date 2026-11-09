package hostclock

import (
	"runtime"
	"testing"
	"time"
)

func TestReadAdvances(t *testing.T) {
	r0, ok := Read()
	if !ok {
		if runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			t.Fatalf("clock pair unavailable on %s", runtime.GOOS)
		}
		t.Skip("no clock pair on this platform")
	}
	time.Sleep(20 * time.Millisecond)
	r1, _ := Read()
	if r1.Awake <= r0.Awake || r1.Total <= r0.Total {
		t.Fatalf("clocks did not advance: %+v → %+v", r0, r1)
	}
	if s := r1.Suspended(r0); s > 50*time.Millisecond {
		t.Fatalf("phantom suspend %v", s)
	}
}

func TestSuspended(t *testing.T) {
	r0 := Reading{Awake: 10 * time.Second, Total: 12 * time.Second}
	r1 := Reading{Awake: 11 * time.Second, Total: 73 * time.Second}
	if got := r1.Suspended(r0); got != 60*time.Second {
		t.Fatalf("got %v", got)
	}
	// Clock granularity can make Total lag Awake slightly: never negative.
	if got := (Reading{Awake: 2, Total: 1}).Suspended(Reading{}); got != 0 {
		t.Fatalf("negative suspend %v", got)
	}
}
