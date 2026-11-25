//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package netscope

// Unsupported platforms: no gateway anchor; wan
// correlation still protects against uplink outages.
func defaultGateways() ([]Gateway, error) { return nil, nil }

func inContainer() bool { return false }
