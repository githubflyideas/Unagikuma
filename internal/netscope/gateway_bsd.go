//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package netscope

import (
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/net/route"
)

func defaultGateways() ([]Gateway, error) {
	rib, err := route.FetchRIB(syscall.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, err
	}
	seen := map[bool]bool{} // is4 → found
	var out []Gateway
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || rm.Flags&syscall.RTF_GATEWAY == 0 || rm.Flags&syscall.RTF_UP == 0 || len(rm.Addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		if !isZero(rm.Addrs[syscall.RTAX_DST]) {
			continue
		}
		if len(rm.Addrs) > syscall.RTAX_NETMASK && rm.Addrs[syscall.RTAX_NETMASK] != nil && !isZero(rm.Addrs[syscall.RTAX_NETMASK]) {
			continue
		}
		gw, ok := addrOf(rm.Addrs[syscall.RTAX_GATEWAY])
		if !ok || seen[gw.Is4()] {
			continue
		}
		seen[gw.Is4()] = true
		name := ""
		if ifi, err := net.InterfaceByIndex(rm.Index); err == nil {
			name = ifi.Name
		}
		out = append(out, Gateway{Addr: gw, Iface: name})
	}
	return out, nil
}

func addrOf(a route.Addr) (netip.Addr, bool) {
	switch v := a.(type) {
	case *route.Inet4Addr:
		return netip.AddrFrom4(v.IP), true
	case *route.Inet6Addr:
		return netip.AddrFrom16(v.IP), true
	}
	return netip.Addr{}, false
}

func isZero(a route.Addr) bool {
	ip, ok := addrOf(a)
	return ok && ip.IsUnspecified()
}

func inContainer() bool { return false }
