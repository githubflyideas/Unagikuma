//go:build darwin

package hostclock

import (
	"time"

	"golang.org/x/sys/unix"
)

// macOS: CLOCK_UPTIME_RAW stops while asleep, CLOCK_MONOTONIC keeps counting.
func read() (Reading, bool) {
	var awake, total unix.Timespec
	if unix.ClockGettime(unix.CLOCK_UPTIME_RAW, &awake) != nil || unix.ClockGettime(unix.CLOCK_MONOTONIC, &total) != nil {
		return Reading{}, false
	}
	return Reading{Awake: time.Duration(awake.Nano()), Total: time.Duration(total.Nano())}, true
}
