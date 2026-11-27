package main

import (
	"errors"
	"fmt"
	"github.com/githubflyideas/unagikuma/internal/app"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Several installations on one machine: each is a directory of its own.
// Starting the program in another directory (or with another --data)
// gives another installation; its port is found by itself.

var heldLock *os.File // the data directory's lock, held until exit

// closeDataLock releases the data directory so another server can start.
func closeDataLock() {
	if heldLock != nil {
		_ = heldLock.Close()
		heldLock = nil
	}
}

const (
	lockFile     = "unagikuma.lock"
	portFile     = app.PortFile // the port this installation found for itself
	portPrevFile = app.PortPrevFile
	portMissFile = app.PortMissFile
	defaultPort  = 8077
	lastAutoPort = 8099
)

// listenAuto takes the address the person asked for (explicit: exactly
// that, or an error), or else this installation's own port: the one it
// found before, or the first free one from 8077 on, remembered for the
// next start so the address stays the same.
func listenAuto(data, addr string, explicit bool) (net.Listener, error) {
	if explicit {
		l, err := net.Listen("tcp", addr)
		if err != nil && errors.Is(err, syscall.EADDRINUSE) {
			return nil, fmt.Errorf("port %s is already in use: stop the other program or choose another address with --listen", addr)
		}
		return l, err
	}
	host := ""
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	// The saved port first, then the one before it (a port changed in the
	// web UI that is taken by now must not leave the console out of reach),
	// then the first free one.
	wanted := readPortFile(filepath.Join(data, portFile))
	var tries []int
	for _, p := range []int{wanted, readPortFile(filepath.Join(data, portPrevFile))} {
		if p > 0 && p < 65536 {
			tries = append(tries, p)
		}
	}
	for p := defaultPort; p <= lastAutoPort; p++ {
		tries = append(tries, p)
	}
	for _, p := range tries {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err != nil {
			continue
		}
		if p != defaultPort || fileExists(filepath.Join(data, portFile)) {
			_ = os.WriteFile(filepath.Join(data, portFile), []byte(strconv.Itoa(p)+"\n"), 0o600)
		}
		if wanted > 0 && p != wanted {
			_ = os.WriteFile(filepath.Join(data, portMissFile), []byte(strconv.Itoa(wanted)+"\n"), 0o600)
		} else {
			_ = os.Remove(filepath.Join(data, portMissFile))
		}
		return l, nil
	}
	return nil, fmt.Errorf("ports %d to %d are all in use: choose an address with --listen", defaultPort, lastAutoPort)
}

func readPortFile(p string) int {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
