//go:build !windows

package main

import (
	"os"
	"syscall"
)

// reexec replaces this process with a fresh copy of itself, keeping the
// pid a supervisor (systemd, docker) watches.
func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
