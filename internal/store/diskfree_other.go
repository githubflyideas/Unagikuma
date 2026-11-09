//go:build !linux && !darwin && !freebsd && !windows

package store

// FreeRatio is unknown on this platform: retention is never shrunk.
func (s *Store) FreeRatio() (float64, bool) { return 0, false }
