package netscope

import (
	"bufio"
	"encoding/hex"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// Gateway is a default route's next hop.
type Gateway struct {
	Addr  netip.Addr
	Iface string
}

// DefaultGateways returns the default gateways of this host (at most one
// per address family, lowest metric first). An empty result means the
// platform is unsupported or there is no default route; callers treat
// "no IPv4 default route" separately via HasDefaultRoute.
func DefaultGateways() ([]Gateway, error) { return defaultGateways() }

// InContainer reports whether this process runs inside a container, where
// the default gateway is a bridge on the host rather than the real router.
func InContainer() bool { return inContainer() }

// parseProcRoute parses Linux /proc/net/route (IPv4, hex little-endian).
func parseProcRoute(r io.Reader) []Gateway {
	type cand struct {
		gw     Gateway
		metric int
	}
	var best *cand
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // header
		}
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue // not a default route
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&0x1 == 0 || flags&0x2 == 0 { // RTF_UP, RTF_GATEWAY
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		addr := netip.AddrFrom4([4]byte{raw[3], raw[2], raw[1], raw[0]})
		metric, _ := strconv.Atoi(f[6])
		if best == nil || metric < best.metric {
			best = &cand{Gateway{Addr: addr, Iface: f[0]}, metric}
		}
	}
	if best == nil {
		return nil
	}
	return []Gateway{best.gw}
}

// parseProcIPv6Route parses Linux /proc/net/ipv6_route.
func parseProcIPv6Route(r io.Reader) []Gateway {
	var best *Gateway
	bestMetric := uint64(1<<64 - 1)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// dst dstlen src srclen nexthop metric refcnt use flags iface
		if len(f) < 10 || f[0] != strings.Repeat("0", 32) || f[1] != "00" {
			continue
		}
		raw, err := hex.DecodeString(f[4])
		if err != nil || len(raw) != 16 {
			continue
		}
		addr := netip.AddrFrom16([16]byte(raw))
		if addr.IsUnspecified() || f[9] == "lo" {
			continue
		}
		flags, err := strconv.ParseUint(f[8], 16, 32)
		if err != nil || flags&0x1 == 0 || flags&0x200 != 0 { // RTF_UP; skip RTF_REJECT
			continue
		}
		metric, _ := strconv.ParseUint(f[5], 16, 64)
		if best == nil || metric < bestMetric {
			best, bestMetric = &Gateway{Addr: addr, Iface: f[9]}, metric
		}
	}
	if best == nil {
		return nil
	}
	return []Gateway{*best}
}

// containerCgroup reports whether a /proc/1/cgroup content names a
// container runtime.
func containerCgroup(s string) bool {
	for _, k := range []string{"docker", "kubepods", "containerd", "libpod", "lxc"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
