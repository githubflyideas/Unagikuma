package engine

import (
	"container/heap"
	"hash/fnv"
	"time"
)

// Scheduler orders monitors by the time their next round is due. It holds
// no clock; the engine loop asks for due names and puts them back with
// their next due time after each round. Not safe for concurrent use.
type Scheduler struct {
	h   schedHeap
	idx map[string]*schedEntry
}

type schedEntry struct {
	name string
	due  time.Time
	i    int
}

type schedHeap []*schedEntry

func (h schedHeap) Len() int { return len(h) }
func (h schedHeap) Less(i, j int) bool {
	if h[i].due.Equal(h[j].due) {
		return h[i].name < h[j].name // deterministic order
	}
	return h[i].due.Before(h[j].due)
}
func (h schedHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].i = i; h[j].i = j }
func (h *schedHeap) Push(x any)   { e := x.(*schedEntry); e.i = len(*h); *h = append(*h, e) }
func (h *schedHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	e.i = -1
	return e
}

// NewScheduler returns an empty scheduler.
func NewScheduler() *Scheduler { return &Scheduler{idx: map[string]*schedEntry{}} }

// Set schedules name at due, adding it or moving it.
func (s *Scheduler) Set(name string, due time.Time) {
	if e, ok := s.idx[name]; ok {
		e.due = due
		heap.Fix(&s.h, e.i)
		return
	}
	e := &schedEntry{name: name, due: due}
	heap.Push(&s.h, e)
	s.idx[name] = e
}

// Remove unschedules name (deleted or paused monitor).
func (s *Scheduler) Remove(name string) {
	if e, ok := s.idx[name]; ok {
		heap.Remove(&s.h, e.i)
		delete(s.idx, name)
	}
}

// Rename moves a schedule to a new name, keeping its due time.
func (s *Scheduler) Rename(oldName, newName string) {
	if e, ok := s.idx[oldName]; ok {
		delete(s.idx, oldName)
		e.name = newName
		s.idx[newName] = e
		heap.Fix(&s.h, e.i)
	}
}

// PopDue removes and returns every name due at or before now, earliest
// first. The caller re-adds each one with Set once its round is planned.
func (s *Scheduler) PopDue(now time.Time) []string {
	var out []string
	for len(s.h) > 0 && !s.h[0].due.After(now) {
		e := heap.Pop(&s.h).(*schedEntry)
		delete(s.idx, e.name)
		out = append(out, e.name)
	}
	return out
}

// Next returns the earliest due time.
func (s *Scheduler) Next() (time.Time, bool) {
	if len(s.h) == 0 {
		return time.Time{}, false
	}
	return s.h[0].due, true
}

// Due returns when name is due.
func (s *Scheduler) Due(name string) (time.Time, bool) {
	if e, ok := s.idx[name]; ok {
		return e.due, true
	}
	return time.Time{}, false
}

// Len returns the number of scheduled monitors.
func (s *Scheduler) Len() int { return len(s.h) }

// Spread returns a stable offset in [0, window) derived from name, so that
// monitors added together (startup, bulk import) do not all fire at once
// and keep the same phase across restarts.
func Spread(name string, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(name))
	return time.Duration(h.Sum64() % uint64(window))
}

// NextDue computes the next due time at a fixed rate from the previous due
// time, so rounds do not drift by their own duration. When the loop fell
// behind by whole intervals (overload, stall), the older slots are skipped
// and reported as missed rather than fired in a burst; the latest slot still
// runs, late.
func NextDue(prevDue, now time.Time, interval time.Duration) (next time.Time, missed int) {
	if interval <= 0 {
		return now, 0
	}
	next = prevDue.Add(interval)
	if !next.Before(now) {
		return next, 0
	}
	missed = int(now.Sub(next) / interval)
	return next.Add(time.Duration(missed) * interval), missed
}
