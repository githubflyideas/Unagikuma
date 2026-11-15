package probe

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/appprofile"
	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// validateValueRules checks a monitor's value rules (warn=, crit=,
// change= and the rest) for a check that reads a number in unit.
func validateValueRules(o opts, unit string) error {
	_, err := valuerule.ParseWith(func(k string) string { return o.str(k, "") }, unit, valueFields(o))
	return err
}

// valueFields are the named numbers a condition in warn= or crit= may use:
// the fields of the application's page, or of Redis's INFO. A check that
// reads one number has none.
func valueFields(o opts) valuerule.Fields {
	var p *appprofile.Profile
	if ap, ok := appProfile(o); ok {
		p = ap
	} else if strings.EqualFold(o.str("proto", ""), "redis") {
		p = redisProfile()
	}
	if p == nil {
		return nil
	}
	return func(name string) (valuerule.Field, bool) {
		f, ok := p.Field(name)
		if !ok {
			return valuerule.Field{}, false
		}
		return valuerule.Field{Unit: f.LimitUnit(), Counter: f.Type == "counter"}, true
	}
}

// ValueFields is valueFields for a spec, for the engine that judges it.
func ValueFields(s Spec) valuerule.Fields { return valueFields(opts(s.Opts)) }

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
