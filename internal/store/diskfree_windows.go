//go:build windows

package store

import "golang.org/x/sys/windows"

// FreeRatio is the share of the data directory's volume still free for
// this user (false when it cannot be read).
func (s *Store) FreeRatio() (float64, bool) {
	p, err := windows.UTF16PtrFromString(s.dir)
	if err != nil {
		return 0, false
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil || total == 0 {
		return 0, false
	}
	return float64(avail) / float64(total), true
}
