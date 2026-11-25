package probe

import (
	"slices"
	"testing"
)

// Every option offered for a type is one its check accepts.
func TestOptionsForAreAccepted(t *testing.T) {
	targets := map[string]string{"HTTP": "https://example.com/", "TCP": "example.com:443", "PING": "example.com", "UDP": "example.com:9999",
		"DOCKER": "nginx", "DOMAIN": "example.com", "EXEC": "check_x"}
	for typ, target := range targets {
		for _, k := range OptionsFor(typ) {
			if k == "header.Authorization" || k == "template" || k == "on-filtered" || k == "family" {
				continue // need a value of their own form, checked elsewhere
			}
			s := Spec{Type: typ, Target: target, Opts: map[string]string{k: "x"}}
			err := Validate(s)
			if err != nil && slices.Contains([]string{"unknown option " + k}, err.Error()) {
				t.Errorf("%s offers %s, which its check refuses", typ, k)
			}
		}
	}
	for _, typ := range []string{"SNMP", "DNS", "NTP", "SYSLOG"} {
		if slices.Contains(OptionsFor(typ), "cert-days") || !slices.Contains(OptionsFor(typ), "authorized") {
			t.Errorf("%s: %v", typ, OptionsFor(typ))
		}
	}
	if !slices.Contains(OptionsFor("SNMP"), "community") || slices.Contains(OptionsFor("PUSH"), "on-filtered") {
		t.Errorf("SNMP %v / PUSH %v", OptionsFor("SNMP"), OptionsFor("PUSH"))
	}
}
