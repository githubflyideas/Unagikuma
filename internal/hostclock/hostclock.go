// Package hostclock reads a pair of host clocks that differ exactly by the
// time the machine spent suspended (lid closed, VM paused by the hypervisor
// on some platforms, hibernation). Comparing how far each advanced between
// two readings tells a suspend apart from a process stall and from a wall
// clock jump, which time.Now alone cannot.
package hostclock

import "time"

// Reading is one sample of both clocks, measured from an arbitrary origin.
type Reading struct {
	// Awake excludes time spent suspended.
	Awake time.Duration
	// Total includes time spent suspended.
	Total time.Duration
}

// Suspended returns how long the host was suspended between r0 and r.
func (r Reading) Suspended(r0 Reading) time.Duration {
	d := (r.Total - r0.Total) - (r.Awake - r0.Awake)
	if d < 0 {
		return 0
	}
	return d
}

// Read samples both clocks. ok is false on platforms without a suitable
// pair; callers then fall back to the Go monotonic clock for both values,
// which still detects stalls but cannot attribute them to a suspend.
func Read() (r Reading, ok bool) { return read() }
