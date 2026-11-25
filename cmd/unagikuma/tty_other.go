//go:build !linux && !darwin && !freebsd && !windows

package main

import "errors"

func isTerminal(int) bool { return false }

func readPassword(int) ([]byte, error) { return nil, errors.New("no terminal") }

func ttyWidth(int) (int, bool) { return 0, false }
