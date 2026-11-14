//go:build windows

package netscope

import (
	"net/netip"
	"sort"
	"unsafe"

	"golang.org/x/sys/windows"
)

// adapters lists the network adapters that are up, with their gateways and
// DNS servers.
func adapters() ([]*windows.IpAdapterAddresses, error) {
	size := uint32(15 << 10)
	for range 3 {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil, err
		}
		var out []*windows.IpAdapterAddresses
		for a := first; a != nil; a = a.Next {
			if a.OperStatus == windows.IfOperStatusUp {
				out = append(out, a)
			}
		}
		// The lowest metric first: the adapter Windows routes through.
		sort.SliceStable(out, func(i, j int) bool { return out[i].Ipv4Metric < out[j].Ipv4Metric })
		return out, nil
	}
	return nil, windows.ERROR_BUFFER_OVERFLOW
}

func addrOf(sa windows.SocketAddress) (netip.Addr, bool) {
	if sa.Sockaddr == nil {
		return netip.Addr{}, false
	}
	a, ok := netip.AddrFromSlice(sa.IP())
	return a.Unmap(), ok && a.IsValid() && !a.IsUnspecified()
}

// defaultGateways reads the gateways of the adapters that are up: at most
// one per address family, from the adapter with the lowest metric.
func defaultGateways() ([]Gateway, error) {
	ads, err := adapters()
	if err != nil {
		return nil, err
	}
	var out []Gateway
	seen := map[bool]bool{}
	for _, a := range ads {
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			ip, ok := addrOf(g.Address)
			if !ok || seen[ip.Is4()] {
				continue
			}
			seen[ip.Is4()] = true
			out = append(out, Gateway{Addr: ip, Iface: windows.UTF16PtrToString(a.FriendlyName)})
		}
	}
	return out, nil
}

func inContainer() bool { return false }

// systemDNS lists the DNS servers of the adapters that are up.
func systemDNS() []string {
	ads, err := adapters()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range ads {
		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			if ip, ok := addrOf(d.Address); ok && !seen[ip.String()] && !ip.IsLinkLocalUnicast() {
				seen[ip.String()] = true
				out = append(out, ip.String())
			}
		}
	}
	return out
}
