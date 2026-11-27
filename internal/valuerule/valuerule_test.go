package valuerule

import (
	"strings"
	"testing"
	"time"
)

func rules(t *testing.T, opts map[string]string, unit string) *Tracker {
	t.Helper()
	r, err := Parse(func(k string) string { return opts[k] }, unit)
	if err != nil {
		t.Fatal(err)
	}
	return &Tracker{Rules: r, Retain: 24 * time.Hour, Window: 10 * time.Minute}
}

func TestLimits(t *testing.T) {
	tr := rules(t, map[string]string{"warn": "35", "crit": "40"}, "°C")
	now := time.Unix(1_700_000_000, 0)
	for _, c := range []struct {
		v    float64
		want Level
	}{{30, OK}, {36, Warn}, {41, Crit}, {34, OK}} {
		now = now.Add(time.Minute)
		if o := tr.Observe(now, map[string]float64{"value": c.v}); o.Level != c.want {
			t.Fatalf("%v: %v %q", c.v, o.Level, o.Detail)
		}
	}
	low := rules(t, map[string]string{"crit": "<10"}, "")
	if o := low.Observe(now, map[string]float64{"value": 5}); o.Level != Crit || !strings.Contains(o.Detail, "limit <10") {
		t.Fatalf("below: %+v", o)
	}
}

// A breach must last "for" before it counts.
func TestFor(t *testing.T) {
	tr := rules(t, map[string]string{"crit": "100", "for": "3m"}, "")
	now := time.Unix(1_700_000_000, 0)
	var last Outcome
	for i := 0; i < 4; i++ {
		now = now.Add(time.Minute)
		last = tr.Observe(now, map[string]float64{"value": 200})
		if i < 3 && last.Level != OK {
			t.Fatalf("round %d: %v", i, last.Level)
		}
	}
	if last.Level != Crit {
		t.Fatalf("after 3m: %v", last.Level)
	}
}

// Counters become rates; a counter going backwards costs one sample.
func TestPortCounters(t *testing.T) {
	tr := rules(t, map[string]string{"value": "util", "warn": "70", "crit": "90"}, "")
	now := time.Unix(1_700_000_000, 0)
	speed := 1e9 // 1 Gbps
	obs := func(in, out float64) Outcome {
		now = now.Add(time.Minute)
		return tr.Observe(now, map[string]float64{"if.in_octets": in, "if.out_octets": out, "if.speed_bps": speed})
	}
	if o := obs(0, 0); o.Level != Skip {
		t.Fatalf("first reading: %v", o.Level)
	}
	// 60 s at 600 Mbit/s in = 4.5e9 bytes: 60%.
	if o := obs(4.5e9, 1e6); o.Level != OK || o.Value < 59.9 || o.Value > 60.1 {
		t.Fatalf("60%%: %+v", o)
	}
	if o := obs(4.5e9+6.9e9, 2e6); o.Level != Crit { // 920 Mbit/s
		t.Fatalf("92%%: %+v", o)
	}
	if o := obs(10, 10); o.Level != Skip { // device restarted
		t.Fatalf("wrap: %+v", o)
	}
	if o := obs(1e6, 1e6); o.Level != OK {
		t.Fatalf("after the wrap: %+v", o)
	}
	speed = 1e8
	if o := obs(2e6, 2e6); o.Level != Warn || !strings.Contains(o.Detail, "link speed fell") {
		t.Fatalf("speed drop: %+v", o)
	}
}

// Change against the same time a day earlier: learning until there is
// one; the floor keeps an idle port quiet.
func TestChangeAgainstYesterday(t *testing.T) {
	tr := rules(t, map[string]string{"change": "50%", "compare": "24h", "min": "10"}, "")
	start := time.Unix(1_700_000_000, 0)
	now := start
	// The reference is the median of the samples within 5 minutes either
	// side of 24 hours ago: learning ends 5 minutes before a full day.
	for now.Before(start.Add(24*time.Hour - 5*time.Minute)) {
		o := tr.Observe(now, map[string]float64{"value": 100})
		if o.Level != Learning {
			t.Fatalf("at %s: %v", now.Sub(start), o.Level)
		}
		now = now.Add(5 * time.Minute)
	}
	now = start.Add(24 * time.Hour)
	if o := tr.Observe(now, map[string]float64{"value": 120}); o.Level != OK || !o.HasRef || o.Ref != 100 {
		t.Fatalf("+20%%: %+v", o)
	}
	now = now.Add(5 * time.Minute)
	if o := tr.Observe(now, map[string]float64{"value": 260}); o.Level != Warn || !strings.Contains(o.Detail, "160% above 100") {
		t.Fatalf("+160%%: %+v", o)
	}
	now = now.Add(5 * time.Minute)
	if o := tr.Observe(now, map[string]float64{"value": 0}); o.Level != Warn {
		t.Fatalf("fell to 0: %+v", o)
	}
	idle := rules(t, map[string]string{"change": "50%", "min": "10"}, "")
	idle.Observe(now, map[string]float64{"value": 1})
	if o := idle.Observe(now.Add(time.Minute), map[string]float64{"value": 5}); o.Level != OK {
		t.Fatalf("idle 1 → 5: %+v", o)
	}
	up := rules(t, map[string]string{"change": "50%", "direction": "up"}, "")
	up.Observe(now, map[string]float64{"value": 100})
	if o := up.Observe(now.Add(time.Minute), map[string]float64{"value": 10}); o.Level != OK {
		t.Fatalf("direction up, a fall: %+v", o)
	}
}

func TestParse(t *testing.T) {
	for _, bad := range []map[string]string{{"warn": "x"}, {"change": "-5%"}, {"compare": "yesterday", "change": "5%"}, {"min": "5"}, {"value": "temp"}, {"direction": "sideways"}, {"warn": "5 parsecs"}} {
		if _, err := Parse(func(k string) string { return bad[k] }, ""); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	for v, want := range map[string]float64{"100Mbps": 1e8, "1.5GB": 1.5 * (1 << 30), "80%": 80, "35°C": 35, "2.5e3": 2500, "-4": -4} {
		if got, err := ParseNumber(v); err != nil || got != want {
			t.Errorf("%s: %v %v", v, got, err)
		}
	}
	if Format(9.2e9, "bps") != "9.2 Gbps" || Format(41, "°C") != "41 °C" || Format(72.456, "%") != "72.46%" {
		t.Error(Format(9.2e9, "bps"), Format(41, "°C"), Format(72.456, "%"))
	}
}

// A limit's unit must measure what the value measures: 500Mbps cannot be
// a limit on a percentage, nor 80% on a temperature.
func TestLimitUnitMustMatch(t *testing.T) {
	get := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		unit string
		opts map[string]string
		ok   bool
	}{
		{"%", map[string]string{"value": "util", "warn": "70"}, true},
		{"%", map[string]string{"value": "util", "warn": "70%"}, true},
		{"%", map[string]string{"value": "util", "warn": "500Mbps"}, false},
		{"bps", map[string]string{"value": "in", "warn": "500Mbps"}, true},
		{"bps", map[string]string{"value": "in", "warn": "80%"}, false},
		{"bps", map[string]string{"value": "in", "warn": "1.5GB"}, false}, // bytes are not a rate
		{"°C", map[string]string{"warn": "35"}, true},
		{"°C", map[string]string{"warn": "35°C"}, true},
		{"°C", map[string]string{"warn": "80%"}, false},
		{"°C", map[string]string{"warn": "100Mbps"}, false},
		{"", map[string]string{"warn": "100"}, true}, // a plain number takes what it is given
		{"", map[string]string{"warn": "5k"}, true},
		{"%", map[string]string{"value": "util", "change": "50%", "min": "10Mbps"}, false},
		{"bps", map[string]string{"value": "in", "change": "50%", "min": "10Mbps"}, true},
	} {
		_, err := Parse(get(c.opts), c.unit)
		if (err == nil) != c.ok {
			t.Errorf("unit %q %v: ok=%v, err=%v", c.unit, c.opts, c.ok, err)
		}
	}
}

// crit must be reached after warn, on the same side.
func TestLimitOrder(t *testing.T) {
	get := func(w, c string) func(string) string {
		return func(k string) string { return map[string]string{"warn": w, "crit": c}[k] }
	}
	for _, c := range []struct {
		warn, crit string
		ok         bool
	}{
		{"70", "90", true}, {"90", "70", false}, {"70", "70", false},
		{">=70", ">70", true},  // warn at exactly 70, crit above it
		{">70", ">=70", false}, // crit already holds at 70: warn never shows
		{"<10", ">5", false},   // nothing between them is fine
		{"<5", ">5", true}, {"<=5", ">5", false}, {">80", "<5", true},
		{"<20", "<10", true}, {"<10", "<20", false},
		{"80", "<5", true}, // different sides: both can apply
		{"", "90", true}, {"70", "", true},
	} {
		if _, err := Parse(get(c.warn, c.crit), ""); (err == nil) != c.ok {
			t.Errorf("warn=%q crit=%q: ok=%v, err=%v", c.warn, c.crit, c.ok, err)
		}
	}
}

func TestInclusiveLimit(t *testing.T) {
	tr := rules(t, map[string]string{"crit": ">=100"}, "")
	now := time.Unix(1_700_000_000, 0)
	if o := tr.Observe(now, map[string]float64{"value": 100}); o.Level != Crit {
		t.Fatalf(">=100 at 100: %v", o.Level)
	}
	if o := rules(t, map[string]string{"crit": "100"}, "").Observe(now, map[string]float64{"value": 100}); o.Level != OK {
		t.Fatalf(">100 at 100: %v", o.Level)
	}
	if o := rules(t, map[string]string{"warn": "<=10"}, "").Observe(now, map[string]float64{"value": 10}); o.Level != Warn {
		t.Fatalf("<=10 at 10: %v", o.Level)
	}
}

// A counter's total only grows, so the rules judge its rate.
func TestCounterValue(t *testing.T) {
	tr := rules(t, map[string]string{"crit": "1000"}, "")
	now := time.Unix(1_700_000_000, 0)
	obs := func(v float64) Outcome {
		now = now.Add(10 * time.Second)
		return tr.Observe(now, map[string]float64{"value": v, "value.counter": 1})
	}
	if o := obs(5_000_000); o.Level != Skip {
		t.Fatalf("first reading: %v", o.Level)
	}
	if o := obs(5_005_000); o.Level != OK || o.Value != 500 { // 500 a second
		t.Fatalf("500/s: %+v", o)
	}
	if o := obs(5_025_000); o.Level != Crit || !strings.Contains(o.Detail, "/s") { // 2000 a second
		t.Fatalf("2000/s: %+v", o)
	}
	if o := obs(100); o.Level != Skip { // restarted
		t.Fatalf("wrap: %+v", o)
	}
}

// A limit must measure what the value measures, and must be reachable.
func TestLimitFitsTheValue(t *testing.T) {
	for _, c := range []struct {
		unit, limit string
		ok          bool
		want        float64
	}{
		{"1", "100", true, 100}, {"1", "2k", true, 2000}, {"1", "80%", false, 0}, {"1", "500Mbps", false, 0},
		{"1", "35C", false, 0}, {"1", "5s", false, 0}, {"1", "1GB", false, 0}, {"1", "-5", false, 0}, {"1", "<0", false, 0},
		{"1", "<=0", true, 0}, {"1", "<1", true, 1},
		{"B", "1GB", true, 1 << 30}, {"B", "500MB", true, 500 << 20}, {"B", "1000000", true, 1e6}, {"B", "80%", false, 0},
		{"s", "300", true, 300}, {"s", "300s", true, 300}, {"s", "1500ms", true, 1.5}, {"s", "1GB", false, 0},
		{"°C", "-5", true, -5}, {"°C", "<0", true, 0}, {"%", "-1", false, 0}, {"bps", "<0", false, 0},
		{"", "80%", true, 80}, {"", "-5", true, -5}, {"", "<0", true, 0}, // what it measures is not known
	} {
		b, err := parseBound(c.limit, c.unit)
		if (err == nil) != c.ok || (err == nil && b.X != c.want) {
			t.Errorf("unit %q limit %q: ok=%v, got %+v, err=%v", c.unit, c.limit, c.ok, b, err)
		}
	}
	if got := Format(1.5*(1<<30), "B"); got != "1.5 GB" {
		t.Errorf("Format bytes = %q", got)
	}
}

// Options that modify a rule need the rule.
func TestModifiersNeedTheirRule(t *testing.T) {
	for _, c := range []struct {
		opts map[string]string
		ok   bool
	}{
		{map[string]string{"for": "5m"}, false},
		{map[string]string{"for": "5m", "warn": "3"}, true},
		{map[string]string{"direction": "up"}, false},
		{map[string]string{"direction": "up", "change": "50%"}, true},
		{map[string]string{"change": "150%", "direction": "down"}, false},
		{map[string]string{"change": "100%", "direction": "down"}, true},
		{map[string]string{"change": "150%"}, true},
	} {
		if _, err := Parse(func(k string) string { return c.opts[k] }, ""); (err == nil) != c.ok {
			t.Errorf("%v: ok=%v, err=%v", c.opts, c.ok, err)
		}
	}
}

// A change from nothing, and from a negative value, is still judged.
func TestChangeFromZeroAndNegative(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tr := rules(t, map[string]string{"change": "50%"}, "")
	tr.Observe(now, map[string]float64{"value": 0})
	if out := tr.Observe(now.Add(time.Minute), map[string]float64{"value": 40}); out.Level != Warn {
		t.Errorf("0 -> 40: %+v", out)
	}
	tr = rules(t, map[string]string{"change": "50%", "direction": "down"}, "")
	tr.Observe(now, map[string]float64{"value": 0})
	if out := tr.Observe(now.Add(time.Minute), map[string]float64{"value": 40}); out.Level != OK {
		t.Errorf("0 -> 40 with direction=down: %+v", out)
	}
	tr = rules(t, map[string]string{"change": "50%"}, "°C")
	tr.Observe(now, map[string]float64{"value": -10})
	if out := tr.Observe(now.Add(time.Minute), map[string]float64{"value": -20}); out.Level != Warn {
		t.Errorf("-10 -> -20: %+v", out)
	}
	if out := tr.Observe(now.Add(2*time.Minute), map[string]float64{"value": -21}); out.Level != OK {
		t.Errorf("-20 -> -21: %+v", out)
	}
}

func fieldsOf(m map[string]Field) Fields {
	return func(n string) (Field, bool) { f, ok := m[n]; return f, ok }
}

var testFields = fieldsOf(map[string]Field{
	"busy":  {Unit: "1"},
	"queue": {Unit: "1"},
	"free":  {Unit: "B"},
	"reqs":  {Unit: "1", Counter: true},
})

func condRules(t *testing.T, opts map[string]string) (*Rules, error) {
	return ParseWith(func(k string) string { return opts[k] }, "1", testFields)
}

// Two or more comparisons with one joiner; anything else is refused.
func TestConditionParse(t *testing.T) {
	for _, c := range []struct {
		crit string
		ok   bool
	}{
		{"busy>50 and queue>100", true},
		{"busy > 50 AND queue >= 100", true},
		{"busy>50 or queue>100 or free<1GB", true},
		{"busy>50 and queue>100 and free<1GB and reqs>5", true},
		{"busy>50 and queue>100 or free<1GB", false}, // mixed: no brackets, no meaning
		{"busy>50 and queue>100 and free<1GB and reqs>5 and busy>1", false},
		{"busy>50 and busy<10", false},   // one field twice
		{"busy>50 and nope>1", false},    // not a field
		{"busy>50 and queue==1", false},  // no equality on numbers
		{"busy>50 and queue>80%", false}, // the unit must fit
		{"busy>50 and free>abc", false},  // not a number
		{"busy>50 and", false},           // a comparison is missing
		{"busy>50 and queue", false},     // no limit
		{"busy>50", false},               // one field: field= and a limit
	} {
		_, err := condRules(t, map[string]string{"crit": c.crit})
		if (err == nil) != c.ok {
			t.Errorf("crit=%q: ok=%v, err=%v", c.crit, c.ok, err)
		}
	}
	// A check without named numbers has nothing to name.
	if _, err := Parse(func(k string) string { return map[string]string{"crit": "busy>5 and queue>5"}[k] }, ""); err == nil {
		t.Error("condition accepted on a check without fields")
	}
}

// "and" needs every comparison, "or" any; warn and crit each take one;
// a counter's comparison is on its rate.
func TestConditionJudge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	obs := func(tr *Tracker, at time.Time, busy, queue, reqs float64) Outcome {
		return tr.Observe(at, map[string]float64{"value": busy, "app.busy": busy, "app.queue": queue, "app.reqs": reqs, "app.free": 5 << 30})
	}
	r, err := condRules(t, map[string]string{"crit": "busy>50 and queue>100", "warn": "busy>50 or queue>100"})
	if err != nil {
		t.Fatal(err)
	}
	tr := &Tracker{Rules: r}
	for _, c := range []struct {
		busy, queue float64
		want        Level
	}{
		{10, 10, OK}, {60, 10, Warn}, {10, 200, Warn}, {60, 200, Crit}, {50, 100, OK},
	} {
		if out := obs(tr, now, c.busy, c.queue, 0); out.Level != c.want {
			t.Errorf("busy %v queue %v: %+v, want level %v", c.busy, c.queue, out, c.want)
		}
	}
	if out := obs(tr, now, 60, 200, 0); !strings.Contains(out.Detail, "busy 60 >50 and queue 200 >100") {
		t.Errorf("reason %q", out.Detail)
	}
	// A counter is compared by its rate: 600 requests in a minute is 10/s.
	r, _ = condRules(t, map[string]string{"crit": "reqs>5 and busy>1"})
	tr = &Tracker{Rules: r}
	if out := obs(tr, now, 3, 0, 1000); out.Level != OK {
		t.Errorf("first reading: %+v", out)
	}
	if out := obs(tr, now.Add(time.Minute), 3, 0, 1600); out.Level != Crit {
		t.Errorf("10/s with busy 3: %+v", out)
	}
	if out := obs(tr, now.Add(2*time.Minute), 3, 0, 1660); out.Level != OK {
		t.Errorf("1/s with busy 3: %+v", out)
	}
	// A field with no number leaves an "and" unconfirmed.
	r, _ = condRules(t, map[string]string{"crit": "busy>50 and queue>100"})
	tr = &Tracker{Rules: r}
	if out := tr.Observe(now, map[string]float64{"value": 60, "app.busy": 60}); out.Level != OK {
		t.Errorf("queue missing: %+v", out)
	}
}

// Parse is ParseWith for a check whose numbers have no names.
func Parse(get func(string) string, unit string) (*Rules, error) {
	return ParseWith(get, unit, nil)
}
