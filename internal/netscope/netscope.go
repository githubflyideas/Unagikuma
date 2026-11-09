// Package netscope infers whether a monitor target is on the local network
// ("lan") or behind the uplink ("wan"), for scope=auto.
package netscope

import (
	"net"
	"net/netip"
	"strings"
)

// Scope of a target.
type Scope string

const (
	LAN Scope = "lan"
	WAN Scope = "wan"
)

var lanPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

var lanSuffixes = []string{".local", ".lan", ".internal", ".home.arpa", ".localdomain", ".intranet", ".corp", ".home"}

// cgnat is the shared address space used by carriers and by overlay VPNs
// such as Tailscale: traffic to it normally depends on the uplink, so it is
// wan unless the route shows it is on-link.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// OfAddr classifies an IP address by address range only.
func OfAddr(a netip.Addr) Scope {
	a = a.Unmap()
	if a.IsUnspecified() {
		return LAN
	}
	for _, p := range lanPrefixes {
		if p.Contains(a) {
			return LAN
		}
	}
	return WAN
}

// OfHost classifies a host name or IP literal without resolving it.
// known=false means the answer depends on DNS resolution (use OfAddrs).
func OfHost(host string) (scope Scope, known bool) {
	host = strings.TrimSuffix(strings.Trim(strings.ToLower(host), "[]"), ".")
	if host == "" {
		return LAN, true
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return OfAddr(a), true
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return LAN, true // single-label names resolve via local search domains
	}
	for _, s := range lanSuffixes {
		if strings.HasSuffix(host, s) {
			return LAN, true
		}
	}
	return WAN, false
}

// OfAddrs classifies a resolved name: lan only if every address is lan.
func OfAddrs(addrs []net.IP) Scope {
	if len(addrs) == 0 {
		return WAN
	}
	for _, ip := range addrs {
		a, ok := netip.AddrFromSlice(ip)
		if !ok || OfAddr(a) == WAN {
			return WAN
		}
	}
	return LAN
}

// tunnelPrefixes are interface name prefixes of overlay/VPN tunnels; targets
// routed through them depend on the uplink and are wan.
var tunnelPrefixes = []string{"tailscale", "wg", "zt", "tun", "utun", "ppp", "ipsec", "nordlynx"}

// IsTunnel reports whether an interface name looks like a VPN/overlay tunnel.
func IsTunnel(name string) bool {
	n := strings.ToLower(name)
	for _, p := range tunnelPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// Iface is the part of a network interface that routing classification needs.
type Iface struct {
	Name     string
	Prefixes []netip.Prefix // configured addresses with their on-link prefix
}

// Interfaces snapshots the host's up interfaces.
func Interfaces() []Iface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []Iface
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		var ps []netip.Prefix
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if p, err := netip.ParsePrefix(ipn.String()); err == nil {
					ps = append(ps, p)
				}
			}
		}
		out = append(out, Iface{Name: ifc.Name, Prefixes: ps})
	}
	return out
}

// Route classifies addr using the host's routing view (scope=auto):
// targets reached through a tunnel interface are wan; targets inside an
// on-link prefix of a non-tunnel interface are lan (including IPv6 GUA and
// CGNAT prefixes that are directly connected); otherwise the address range
// decides, with CGNAT treated as wan. src is the local source address the
// kernel would use for addr (see SourceFor); it may be invalid.
func Route(addr, src netip.Addr, ifs []Iface) Scope {
	addr = addr.Unmap()
	if src.IsValid() {
		for _, ifc := range ifs {
			for _, p := range ifc.Prefixes {
				if p.Addr().Unmap() == src.Unmap() && IsTunnel(ifc.Name) {
					return WAN
				}
			}
		}
	}
	for _, ifc := range ifs {
		if IsTunnel(ifc.Name) {
			continue
		}
		for _, p := range ifc.Prefixes {
			if p.Masked().Contains(addr) && !p.Addr().IsLoopback() || addr.IsLoopback() {
				return LAN
			}
		}
	}
	if cgnat.Contains(addr) {
		return WAN
	}
	return OfAddr(addr)
}

// SourceFor asks the kernel which local address it would use to reach addr,
// without sending any packet (a connected UDP socket only selects a route).
func SourceFor(addr netip.Addr) netip.Addr {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr, 9)))
	if err != nil {
		return netip.Addr{}
	}
	defer c.Close()
	if la, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return la.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}
