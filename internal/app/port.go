package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// The port, changed in the web UI: saved for the next start, never switched
// while running. The program takes the saved port when it starts; if that
// port is taken then, it goes back to the one before (see the server
// command), so a change can never leave the console out of reach.

const (
	PortFile     = "listen.port"
	PortPrevFile = "listen.port.prev"
	PortMissFile = "listen.port.missed" // the port wanted at the last start, when it was taken
)

// PortInfo is the port standing for the settings page.
type PortInfo struct {
	Current int    `json:"current"`
	Next    int    `json:"next,omitempty"`   // saved, takes effect at the next start
	Missed  int    `json:"missed,omitempty"` // wanted at the last start but taken
	Fixed   bool   `json:"fixed,omitempty"`  // set by --listen: change it there
	Master  bool   `json:"master,omitempty"` // a cluster's master: nodes keep its address
	DataDir string `json:"data_dir"`
	// CanRestart: the console may offer to restart now.
	CanRestart bool `json:"can_restart,omitempty"`
}

func readPort(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return p
}

// PortInfo reports the port standing.
func (a *App) PortInfo() PortInfo {
	_, port, _ := net.SplitHostPort(a.opts.Listen)
	cur, _ := strconv.Atoi(port)
	pi := PortInfo{Current: cur, Fixed: a.opts.PortFixed, DataDir: a.opts.DataDir, Missed: readPort(a.dataPath(PortMissFile))}
	if n := readPort(a.dataPath(PortFile)); n != 0 && n != cur {
		pi.Next = n
	}
	pi.CanRestart = a.opts.CanRestart
	pi.Master = !a.isMember() && IsCluster(a.EffectiveConfig())
	return pi
}

var (
	ErrPortFixed  = errors.New("the port is set with --listen (or UNAGIKUMA_LISTEN) where the program is started: change it there")
	ErrPortMaster = errors.New("this is the master of a cluster: its nodes keep its address, so its port is not changed here")
)

// SetPort saves the port for the next start, if it is free now.
func (a *App) SetPort(p int) error {
	pi := a.PortInfo()
	switch {
	case pi.Fixed:
		return ErrPortFixed
	case pi.Master:
		return ErrPortMaster
	case p < 1024 || p > 65535:
		return fmt.Errorf("choose a port from 1024 to 65535")
	}
	if p != pi.Current {
		host, _, _ := net.SplitHostPort(a.opts.Listen)
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err != nil {
			return fmt.Errorf("port %d is in use by another program", p)
		}
		l.Close()
	}
	if err := os.WriteFile(a.dataPath(PortPrevFile), []byte(strconv.Itoa(pi.Current)+"\n"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(a.dataPath(PortMissFile))
	return os.WriteFile(a.dataPath(PortFile), []byte(strconv.Itoa(p)+"\n"), 0o600)
}

// Restart restarts the program (as the server command does for a takeover).
func (a *App) Restart() { a.requestRestart() }
