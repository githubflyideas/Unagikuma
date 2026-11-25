//go:build !linux && !darwin && !windows

package hostclock

func read() (Reading, bool) { return Reading{}, false }
