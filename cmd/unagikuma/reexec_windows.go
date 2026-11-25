//go:build windows

package main

import (
	"errors"
	"os"
)

// reexec ends the process with a failure code: the service manager
// restarts it (the service is installed with restart on failure).
func reexec() error {
	os.Exit(3)
	return errors.New("unreachable")
}
