//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockDataDir: see datalock_unix.go.
func lockDataDir(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false, err
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err != nil {
		f.Close()
		return false, nil
	}
	heldLock = f
	return true, nil
}
