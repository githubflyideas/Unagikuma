package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
)

// classify maps a network error to a vote and reason. udp selects the UDP
// meaning of a refusal (ICMP port unreachable rather than a TCP RST).
func classify(err error, udp bool, o opts) Result {
	var de *net.DNSError
	switch {
	case err == nil:
		return ok("")
	case errors.As(err, &de):
		if de.IsNotFound {
			return fail(ReasonDNSNXDomain, "%s does not resolve (no such name)", de.Name)
		}
		return fail(ReasonDNSResolve, "cannot resolve %s: %s", de.Name, de.Err)
	case errors.Is(err, errMetadata):
		return abstain(ReasonConfig, "%v", err)
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EADDRNOTAVAIL):
		// This host has no route: never blame the target.
		return abstain(ReasonNoRoute, "no route from this host (%v)", rootErr(err))
	case isConnRefused(err):
		if udp {
			return fail(ReasonPortUnreachable, "ICMP port unreachable: nothing listens on that UDP port")
		}
		return fail(ReasonConnRefused, "connection refused")
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.EHOSTDOWN):
		return fail(ReasonHostUnreachable, "host unreachable")
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		// Administratively prohibited (a firewall said no).
		if o.str("on-filtered", "fail") == "abstain" {
			return abstain(ReasonFiltered, "blocked by a firewall (administratively prohibited)")
		}
		return fail(ReasonFiltered, "blocked by a firewall (administratively prohibited)")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), isTimeout(err):
		return fail(ReasonTimeout, "no response within the timeout")
	case errors.Is(err, syscall.ECONNRESET):
		return fail(ReasonProtoError, "connection reset by the peer")
	}
	return fail(ReasonProtoError, "%v", rootErr(err))
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// rootErr strips net.OpError/os.SyscallError wrapping for short details.
func rootErr(err error) error {
	for {
		var oe *net.OpError
		var se *os.SyscallError
		switch {
		case errors.As(err, &oe) && oe.Err != nil && oe.Err != err:
			err = oe.Err
		case errors.As(err, &se) && se.Err != nil && se.Err != err:
			err = se.Err
		default:
			return err
		}
	}
}

// errMetadata guards cloud metadata endpoints.
var errMetadata = errors.New("refusing to reach a cloud metadata address (set allow-metadata=yes if this is intended)")

var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("100.100.100.200"),
	netip.MustParseAddr("fd00:ec2::254"),
}

func isMetadata(a netip.Addr) bool {
	a = a.Unmap()
	for _, m := range metadataAddrs {
		if a == m {
			return true
		}
	}
	return false
}

// resolve returns the address to probe: an IP literal, or the first
// resolved address of the requested family. Monitor definitions can be
// edited from the Web UI, so cloud metadata addresses are refused unless
// explicitly allowed.
// lookupNetIP resolves names; tests replace it so they need no network.
var lookupNetIP = net.DefaultResolver.LookupNetIP

func resolve(ctx context.Context, host string, o opts) (netip.Addr, error) {
	host = strings.Trim(host, "[]")
	family := o.str("family", "")
	check := func(a netip.Addr) (netip.Addr, error) {
		a = a.Unmap()
		if isMetadata(a) && !o.on("allow-metadata") {
			return netip.Addr{}, errMetadata
		}
		return a, nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if (family == "4" && !a.Unmap().Is4()) || (family == "6" && a.Unmap().Is4()) {
			return netip.Addr{}, fmt.Errorf("address %s is not IPv%s", a, family)
		}
		return check(a)
	}
	network := "ip"
	if family != "" {
		network = "ip" + family
	}
	addrs, err := lookupNetIP(ctx, network, host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(addrs) == 0 {
		return netip.Addr{}, &net.DNSError{Err: "no address of the requested family", Name: host, IsNotFound: true}
	}
	return check(addrs[0])
}
