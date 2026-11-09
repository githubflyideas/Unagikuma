//go:build !windows

package probe

import (
	"errors"
	"net"
	"syscall"
)

func isConnRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

// isAddrInUse reports a local address (a fixed source port) already taken.
func isAddrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }

// enableUDPErrors is a no-op: Unix kernels report ICMP port unreachable on
// connected UDP sockets as ECONNREFUSED.
func enableUDPErrors(*net.UDPConn) bool { return true }
