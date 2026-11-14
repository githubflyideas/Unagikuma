package probe

import (
	"strings"

	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// validateValueRules checks a monitor's value rules (warn=, crit=,
// change= and the rest) for a check that reads a number in unit.
func validateValueRules(o opts, unit string) error {
	_, err := valuerule.Parse(func(k string) string { return o.str(k, "") }, unit)
	return err
}

// ValueUnit is the unit of the number a spec reads: what its value rules
// compare. "" when it reads none (or a plain number).
func ValueUnit(s Spec) string {
	if strings.EqualFold(s.Type, "SNMP") || strings.EqualFold(s.Type, "UDP") {
		return snmpUnit(opts(s.Opts))
	}
	return ""
}

func snmpUnit(o opts) string {
	if o.has("sensor") {
		return "°C"
	}
	return ""
}
