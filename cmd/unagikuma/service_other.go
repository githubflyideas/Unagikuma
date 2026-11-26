//go:build !windows

package main

import "errors"

func setServiceName(string) {}

func runAsService(func() error) (bool, error) { return false, nil }

func runService([]string) error {
	return errors.New("services are a Windows feature here; on Linux use the systemd unit from the README or the node's install command")
}

// canRestart: the server command re-runs itself in place.
func canRestart() bool { return true }
