//go:build windows

package probe

import (
	"errors"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows reports ICMP port unreachable on UDP as WSAECONNRESET.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, windows.WSAECONNRESET) || errors.Is(err, windows.WSAECONNREFUSED)
}

// isAddrInUse reports a local address (a fixed source port) already taken.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, windows.WSAEADDRINUSE)
}

// enableUDPErrors turns SIO_UDP_CONNRESET back on (the Go runtime turns it
// off for every UDP socket), so a closed port shows up as an error instead
// of silence. It reports whether that worked.
func enableUDPErrors(c *net.UDPConn) bool {
	rc, err := c.SyscallConn()
	if err != nil {
		return false
	}
	okSet := false
	_ = rc.Control(func(fd uintptr) {
		flag := uint32(1)
		var ret uint32
		err := windows.WSAIoctl(windows.Handle(fd), windows.SIO_UDP_CONNRESET, (*byte)(unsafe.Pointer(&flag)), uint32(unsafe.Sizeof(flag)), nil, 0, &ret, nil, 0)
		okSet = err == nil
	})
	return okSet
}
