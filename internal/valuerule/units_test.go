package valuerule

import (
	"testing"
	"time"
)

// Limits mean one thing only: m on a time is minutes, a small b is
// refused (bits or bytes?), days work in every time, and no temperature
// is below absolute zero.
func TestUnambiguousUnits(t *testing.T) {
	for v, want := range map[string]float64{"300": 300, "5m": 300, "500ms": 0.5, "2h": 7200, "1d": 86400, "1m30s": 90} {
		if got, err := ParseQuantity(v, "s"); err != nil || got != want {
			t.Errorf("time %s: %v %v, want %v", v, got, err, want)
		}
	}
	for _, v := range []string{"1Gb", "5mb", "10kb", "1b"} {
		if _, err := ParseNumber(v); err == nil {
			t.Errorf("%s accepted", v)
		}
	}
	for v, want := range map[string]float64{"1GB": 1 << 30, "1Gbps": 1e9, "10kbps": 1e4} {
		if got, err := ParseNumber(v); err != nil || got != want {
			t.Errorf("%s: %v %v", v, got, err)
		}
	}
	for v, want := range map[string]time.Duration{"1d12h": 36 * time.Hour, "2d": 48 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := ParseSpan(v); err != nil || got != want {
			t.Errorf("span %s: %v %v", v, got, err)
		}
	}
	for _, v := range []string{"1x", "1d-12h", "1d+1h", "d", "1.5d"} {
		if _, err := ParseSpan(v); err == nil {
			t.Errorf("%s accepted", v)
		}
	}
	if _, err := ParseQuantity("-300", "°C"); err == nil {
		t.Error("-300 °C accepted")
	}
	if got, err := ParseQuantity("-20", "°C"); err != nil || got != -20 {
		t.Errorf("-20 °C: %v %v", got, err)
	}
}
