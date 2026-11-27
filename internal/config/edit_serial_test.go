package config

import (
	"strings"
	"testing"
	"time"
)

// TestNextSerialHundredthChange: the 100th change of a day goes on to the
// next day's numbers, stays above the one before, and says so.
func TestNextSerialHundredthChange(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	n, warn, err := NextSerial(2026112798, now)
	if err != nil || n != 2026112799 || warn != "" {
		t.Fatalf("99th: %d %q %v", n, warn, err)
	}
	n, warn, err = NextSerial(2026112799, now)
	if err != nil || n != 2026112800 || !strings.Contains(warn, "more than 99 changes today") {
		t.Fatalf("100th: %d %q %v", n, warn, err)
	}
	n, _, _ = NextSerial(n, now.Add(24*time.Hour))
	if n != 2026112801 {
		t.Fatalf("next day after the overflow: %d", n)
	}
}
