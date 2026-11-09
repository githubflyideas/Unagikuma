package app

import (
	"net"
	"strings"
)

// advertiseServerURL is the https://host:port peers should dial for this
// process. Listen may be ":8077" or "0.0.0.0:8077"; those cannot go into a
// join code, so we fall back to base_url, then a non-loopback IPv4, then
// loopback (same-host / lab).
func (a *App) advertiseServerURL() string {
	if u := strings.TrimRight(strings.TrimSpace(a.baseURL()), "/"); strings.HasPrefix(u, "https://") {
		return u
	}
	host, port, err := net.SplitHostPort(a.opts.Listen)
	if err != nil {
		return ""
	}
	if host != "" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
			return "https://" + net.JoinHostPort(host, port)
		}
	}
	if ip := firstAdvertisableIPv4(); ip != "" {
		return "https://" + net.JoinHostPort(ip, port)
	}
	return "https://" + net.JoinHostPort("127.0.0.1", port)
}

func firstAdvertisableIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() && !ip4.IsUnspecified() {
				return ip4.String()
			}
		}
	}
	return ""
}
