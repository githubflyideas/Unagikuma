//go:build linux

package netscope

import (
	"errors"
	"os"
)

func defaultGateways() ([]Gateway, error) {
	var out []Gateway
	f4, err4 := os.Open("/proc/net/route")
	if err4 == nil {
		out = append(out, parseProcRoute(f4)...)
		f4.Close()
	}
	if f6, err := os.Open("/proc/net/ipv6_route"); err == nil {
		out = append(out, parseProcIPv6Route(f6)...)
		f6.Close()
	}
	if err4 != nil && len(out) == 0 {
		return nil, errors.New("cannot read the routing table")
	}
	return out, nil
}

func inContainer() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	if os.Getenv("container") != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	return err == nil && containerCgroup(string(b))
}
