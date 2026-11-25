package engine

import (
	"reflect"
	"testing"
	"time"
)

func TestSchedulerOrder(t *testing.T) {
	s := NewScheduler()
	s.Set("c", t0.Add(3*time.Second))
	s.Set("a", t0.Add(time.Second))
	s.Set("b", t0.Add(2*time.Second))
	s.Set("a2", t0.Add(time.Second))
	if n, _ := s.Next(); !n.Equal(t0.Add(time.Second)) {
		t.Fatalf("next %v", n)
	}
	if got := s.PopDue(t0.Add(2 * time.Second)); !reflect.DeepEqual(got, []string{"a", "a2", "b"}) {
		t.Fatalf("due %v", got)
	}
	if s.Len() != 1 {
		t.Fatalf("len %d", s.Len())
	}
	s.Set("c", t0) // move earlier
	if got := s.PopDue(t0); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("moved %v", got)
	}
	if _, ok := s.Next(); ok {
		t.Fatal("empty")
	}
}

func TestSchedulerRemoveRename(t *testing.T) {
	s := NewScheduler()
	s.Set("a", t0)
	s.Set("b", t0.Add(time.Second))
	s.Remove("a")
	s.Remove("nope")
	s.Rename("b", "web")
	if d, ok := s.Due("web"); !ok || !d.Equal(t0.Add(time.Second)) {
		t.Fatalf("rename: %v %v", d, ok)
	}
	if _, ok := s.Due("b"); ok {
		t.Fatal("old name kept")
	}
	if got := s.PopDue(t0.Add(time.Hour)); !reflect.DeepEqual(got, []string{"web"}) {
		t.Fatalf("%v", got)
	}
}

func TestSpread(t *testing.T) {
	w := time.Minute
	seen := map[time.Duration]bool{}
	for _, n := range []string{"web", "dns-core", "ntp1", "nas", "router"} {
		d := Spread(n, w)
		if d < 0 || d >= w {
			t.Fatalf("out of range %v", d)
		}
		if d != Spread(n, w) {
			t.Fatal("not stable")
		}
		seen[d] = true
	}
	if len(seen) < 4 {
		t.Fatalf("poor spread: %v", seen)
	}
	if Spread("x", 0) != 0 {
		t.Fatal("zero window")
	}
}

func TestNextDue(t *testing.T) {
	iv := time.Minute
	if next, missed := NextDue(t0, t0.Add(5*time.Second), iv); !next.Equal(t0.Add(iv)) || missed != 0 {
		t.Fatalf("on time: %v %d", next, missed)
	}
	// Exactly due now: run it now, nothing missed.
	if next, missed := NextDue(t0, t0.Add(iv), iv); !next.Equal(t0.Add(iv)) || missed != 0 {
		t.Fatalf("boundary: %v %d", next, missed)
	}
	// 3.5 intervals after the previous slot: the slots at 1m and 2m are
	// skipped, the 3m slot runs late; never a burst.
	next, missed := NextDue(t0, t0.Add(3*iv+30*time.Second), iv)
	if !next.Equal(t0.Add(3*iv)) || missed != 2 {
		t.Fatalf("behind: %v %d", next, missed)
	}
	// Slightly late (less than an interval): run late, nothing missed.
	if next, missed := NextDue(t0, t0.Add(iv+time.Second), iv); !next.Equal(t0.Add(iv)) || missed != 0 {
		t.Fatalf("late: %v %d", next, missed)
	}
	if next, _ := NextDue(t0, t0, 0); !next.Equal(t0) {
		t.Fatal("zero interval")
	}
}
