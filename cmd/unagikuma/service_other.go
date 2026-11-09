//go:build !windows

package main

import "errors"

func runAsService(func() error) (bool, error) { return false, nil }

func runService([]string) error {
	return errors.New("services are a Windows feature here; on Linux use the systemd unit from the README or the node's install command")
}
