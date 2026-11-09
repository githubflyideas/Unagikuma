package netscope

import (
	"net"
	"net/netip"
	"testing"
)

func TestOfHost(t *testing.T) {
	cases := map[string]struct {
		s     Scope
		known bool
	}{
		"192.168.1.10": {LAN, true}, "10.0.0.53": {LAN, true}, "100.64.1.1": {WAN, true},
		"8.8.8.8": {WAN, true}, "[fd00::1]": {LAN, true}, "2001:db8::1": {WAN, true},
		"nas.local": {LAN, true}, "router.home.arpa": {LAN, true}, "nas": {LAN, true},
		"example.com": {WAN, false},
	}
	for h, want := range cases {
		s, k := OfHost(h)
		if s != want.s || k != want.known {
			t.Errorf("%s: %s/%v want %s/%v", h, s, k, want.s, want.known)
		}
	}
	if OfAddrs([]net.IP{net.ParseIP("10.1.1.1"), net.ParseIP("1.1.1.1")}) != WAN {
		t.Error("mixed should be wan")
	}
	if OfAddrs([]net.IP{net.ParseIP("10.1.1.1")}) != LAN {
		t.Error("private should be lan")
	}
}

func TestRoute(t *testing.T) {
	ifs := []Iface{
		{Name: "eth0", Prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.20/24"), netip.MustParsePrefix("2001:db8:1::20/64")}},
		{Name: "tailscale0", Prefixes: []netip.Prefix{netip.MustParsePrefix("100.101.1.2/32")}},
		{Name: "lo", Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}},
	}
	cases := []struct {
		addr, src string
		want      Scope
	}{
		{"192.168.1.10", "192.168.1.20", LAN},    // on-link
		{"2001:db8:1::5", "2001:db8:1::20", LAN}, // on-link IPv6 GUA
		{"100.90.0.7", "100.101.1.2", WAN},       // via tailscale
		{"10.8.0.1", "100.101.1.2", WAN},         // private, but routed through a tunnel
		{"10.9.0.1", "192.168.1.20", LAN},        // routed private network
		{"8.8.8.8", "192.168.1.20", WAN},         // internet
		{"100.64.5.5", "192.168.1.20", WAN},      // CGNAT not on-link
		{"127.0.0.1", "127.0.0.1", LAN},
	}
	for _, c := range cases {
		got := Route(netip.MustParseAddr(c.addr), netip.MustParseAddr(c.src), ifs)
		if got != c.want {
			t.Errorf("%s via %s: %s want %s", c.addr, c.src, got, c.want)
		}
	}
}
