package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func isTerminal(fd int) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}

// readPassword reads a line from the console with echo turned off, and
// restores the console mode afterwards.
func readPassword(fd int) ([]byte, error) {
	h := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	quiet := old&^windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT
	if err := windows.SetConsoleMode(h, quiet); err != nil {
		return nil, err
	}
	defer func() { _ = windows.SetConsoleMode(h, old) }()
	return readTTYLine(os.Stdin)
}

// ttyWidth is the console's width in columns.
func ttyWidth(fd int) (int, bool) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info); err != nil {
		return 0, false
	}
	return int(info.Window.Right - info.Window.Left + 1), true
}
