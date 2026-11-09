//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package netscope

// Unsupported platforms (Windows for now): no gateway anchor; wan
// correlation still protects against uplink outages.
func defaultGateways() ([]Gateway, error) { return nil, nil }

func inContainer() bool { return false }
