//go:build linux

package hostclock

import (
	"time"

	"golang.org/x/sys/unix"
)

// Linux: CLOCK_MONOTONIC stops while suspended, CLOCK_BOOTTIME does not.
func read() (Reading, bool) {
	var awake, total unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &awake) != nil || unix.ClockGettime(unix.CLOCK_BOOTTIME, &total) != nil {
		return Reading{}, false
	}
	return Reading{Awake: time.Duration(awake.Nano()), Total: time.Duration(total.Nano())}, true
}
