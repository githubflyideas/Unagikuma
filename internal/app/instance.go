package app

import (
	"net"
	"path/filepath"
	"strings"
)

// DefaultInstanceName is the name shown in notifications when none is
// set: the host name, plus the data directory's last component when that
// is not a generic name ("data", "unagikuma"), otherwise the listen port
// when it is not the default 8077. Two installations on one machine then
// do not look like one in alerts. A single installation on port 8077
// with the usual data directory keeps the host name alone, as before.
func DefaultInstanceName(dataDir, listen, hostname string) string {
	if hostname == "" {
		hostname = "unagikuma"
	}
	base := filepath.Base(filepath.Clean(dataDir))
	switch strings.ToLower(base) {
	case "", ".", "data", "unagikuma":
		_, port, err := net.SplitHostPort(listen)
		if err == nil && port != "" && port != "8077" {
			return hostname + "-" + port
		}
		return hostname
	default:
		return hostname + "-" + base
	}
}
