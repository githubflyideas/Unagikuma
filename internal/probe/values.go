package probe

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// validateValueRules checks a monitor's value rules (warn=, crit=,
// change= and the rest) for a check that reads a number in unit.
func validateValueRules(o opts, unit string) error {
	_, err := valuerule.Parse(func(k string) string { return o.str(k, "") }, unit)
	return err
}

// rulesSet reports whether the monitor has a rule that judges a number.
func rulesSet(o opts) bool { return o.has("warn") || o.has("crit") || o.has("change") }

// isNumber reports whether a written expectation is a number.
func isNumber(v string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return err == nil
}

// textAndNumber refuses judging one value both as a text and as a number:
// a value that must be the text "ok" has no size to compare. text is the
// option that expects a text, num the one that compares a number.
func textAndNumber(o opts, text, num string) error {
	if !o.has(text) {
		return nil
	}
	if o.has(num) {
		return fmt.Errorf("%s= and %s= judge the same value: use one of them", text, num)
	}
	if rulesSet(o) && !isNumber(o.str(text, "")) {
		return fmt.Errorf("%s=%s expects a text, and warn=, crit= and change= judge a number: one value cannot be both", text, o.str(text, ""))
	}
	return nil
}

// ValueUnit is the unit of the number a spec reads: what its value rules
// compare. "" when it reads none (or a plain number).
func ValueUnit(s Spec) string {
	if strings.EqualFold(s.Type, "SNMP") || strings.EqualFold(s.Type, "UDP") {
		return snmpUnit(opts(s.Opts))
	}
	if strings.EqualFold(s.Type, "HTTP") {
		return appUnit(opts(s.Opts))
	}
	if strings.EqualFold(s.Type, "TCP") && strings.EqualFold(s.Opts["proto"], "redis") {
		if f, ok := redisField(opts(s.Opts)); ok {
			return f.LimitUnit()
		}
	}
	return ""
}

func snmpUnit(o opts) string {
	if o.has("sensor") {
		return "°C"
	}
	return ""
}
