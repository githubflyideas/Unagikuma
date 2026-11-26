//go:build unix

package main

import (
	"os"
	"syscall"
)

// lockDataDir holds an exclusive lock on the data directory for as long
// as the server runs: a second server on the same directory is refused
// instead of sharing its databases. The lock goes with the process.
func lockDataDir(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false, nil
	}
	heldLock = f
	return true, nil
}
