//go:build linux || darwin || freebsd

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	return err == nil
}

// readPassword reads a line from the terminal with echo turned off, and
// restores the terminal afterwards.
func readPassword(fd int) ([]byte, error) {
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, err
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	quiet.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &quiet); err != nil {
		return nil, err
	}
	defer func() { _ = unix.IoctlSetTermios(fd, ioctlWriteTermios, old) }()
	return readTTYLine(os.NewFile(uintptr(fd), "tty"))
}

// ttyWidth is the terminal's width in columns.
func ttyWidth(fd int) (int, bool) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 0, false
	}
	return int(ws.Col), true
}
