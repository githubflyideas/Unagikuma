package main

import (
	"testing"
	"time"
)

func TestParseAuditRetention(t *testing.T) {
	day := 24 * time.Hour
	for v, want := range map[string]time.Duration{"": 0, "30d": 30 * day, "3650d": 3650 * day, "1000d": 1000 * day} {
		if got, err := parseAuditRetention(v); err != nil || got != want {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
	for _, v := range []string{"29d", "3661d", "1h", "soon"} {
		if _, err := parseAuditRetention(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}
