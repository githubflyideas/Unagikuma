//go:build linux || darwin || freebsd

package store

import "golang.org/x/sys/unix"

// FreeRatio is the share of the data directory's file system still free
// for this user (false when it cannot be read).
func (s *Store) FreeRatio() (float64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(s.dir, &st); err != nil || st.Blocks == 0 {
		return 0, false
	}
	return float64(st.Bavail) / float64(st.Blocks), true
}
