// Package valuerule judges a number a check reads (a temperature, a port's
// traffic, a value in a JSON reply) by the same rules for every kind of
// check: fixed limits, a change against a reference (the previous sample,
// or the same time a day earlier), a floor below which changes do not
// count, and how long a breach must last. Counters (bytes on a port) are
// turned into rates first; a counter that goes backwards (wrapped, or the
// device restarted) costs one sample and never an alert.
package valuerule

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Level is the outcome of one judgement.
type Level int

const (
	OK       Level = iota
	Learning       // no reference yet: nothing is judged
	Skip           // no value this round (first counter sample, wrap)
	Missing        // the check reported no value at all: the rules cannot work
	Warn
	Crit
)

// Bound is a limit: the value must stay below it, or above it with Below.
type Bound struct {
	X     float64
	Below bool // "<10": a value under 10 breaches
	Incl  bool // "<=10" / ">=10": the limit itself breaches too
}

func (b Bound) breached(v float64) bool {
	if b.Below {
		return v < b.X || (b.Incl && v == b.X)
	}
	return v > b.X || (b.Incl && v == b.X)
}

// A Field is a named number a check reads (a field of an application's
// page), which a condition may name.
type Field struct {
	Unit    string // the unit its limits are written in
	Counter bool   // a total that only grows: judged by its rate
}

// Fields resolves the names a condition may use; nil when the check has
// no named numbers.
type Fields func(name string) (Field, bool)

// A Term is one comparison of a condition: busy>50, free<1GB.
type Term struct {
	Name  string
	Field Field
	Bound Bound
}

// A Cond combines two to MaxTerms comparisons with one joiner, all "and"
// or all "or": busy>50 and queue>100. It is the value of warn= or crit=
// in place of a single limit.
type Cond struct {
	Terms []Term
	Or    bool
	Text  string
}

// MaxTerms is how many comparisons one condition holds.
const MaxTerms = 4

// Rules are the options of one monitor.
type Rules struct {
	WarnCond, CritCond *Cond
	// Value names the number judged: "" (the check's own value), or for a
	// network port "in", "out" or "util".
	Value      string
	Warn, Crit *Bound
	Change     float64       // fraction (0.5 = 50%); 0 = off
	Compare    time.Duration // 0 = the previous sample
	Min        float64       // below this on both sides, changes do not count
	For        time.Duration // a breach must last this long
	Direction  string        // up | down | both
	Unit       string        // shown with the value
}

// Keys are the options a monitor takes for the rules.
var Keys = []string{"value", "warn", "crit", "change", "compare", "min", "for", "direction"}

// Active reports whether any rule is set.
func (r *Rules) Active() bool {
	return r != nil && (r.Warn != nil || r.Crit != nil || r.WarnCond != nil || r.CritCond != nil || r.Change > 0)
}

// Parse reads the rules from a monitor's options (get returns "" for an
// option that is not set). unit is the unit of the check's own value.
func Parse(get func(string) string, unit string) (*Rules, error) {
	return ParseWith(get, unit, nil)
}

// ParseWith is Parse for a check whose numbers have names: warn= and crit=
// may then be a condition on several of them.
func ParseWith(get func(string) string, unit string, fields Fields) (*Rules, error) {
	r := &Rules{Value: get("value"), Direction: "both", Unit: unit}
	switch r.Value {
	case "":
	case "in", "out":
		r.Unit = "bps"
	case "util":
		r.Unit = "%"
	default:
		return nil, fmt.Errorf("value must be in, out or util, got %q", r.Value)
	}
	var err error
	for _, k := range []string{"warn", "crit"} {
		v := get(k)
		if !isCondition(v) {
			if i := strings.IndexAny(v, "<>"); i > 0 && strings.Trim(v[:i], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_ ") == "" && strings.TrimSpace(v[:i]) != "" {
				return nil, fmt.Errorf("%s: %q compares one field: name it with field= and write the limit alone, or join two comparisons with and / or", k, v)
			}
			continue
		}
		c, err := parseCond(v, fields)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if k == "warn" {
			r.WarnCond = c
		} else {
			r.CritCond = c
		}
	}
	if r.WarnCond == nil {
		if r.Warn, err = parseBound(get("warn"), r.Unit); err != nil {
			return nil, fmt.Errorf("warn: %w", err)
		}
	}
	if r.CritCond == nil {
		if r.Crit, err = parseBound(get("crit"), r.Unit); err != nil {
			return nil, fmt.Errorf("crit: %w", err)
		}
	}
	if err := checkBounds(r.Warn, r.Crit, get("warn"), get("crit")); err != nil {
		return nil, err
	}
	if v := get("change"); v != "" {
		f, err := ParseNumber(strings.TrimSuffix(v, "%"))
		if err != nil || f <= 0 {
			return nil, fmt.Errorf("change must be a percentage such as 50%%, got %q", v)
		}
		r.Change = f / 100
	}
	if v := get("compare"); v != "" && v != "previous" {
		d, err := ParseSpan(v)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("compare must be previous, or a time such as 24h, got %q", v)
		}
		if d > time.Minute { // a minute or less is the previous sample
			r.Compare = d
		}
	}
	if v := get("min"); v != "" {
		f, err := ParseQuantity(v, r.Unit)
		if err != nil || f < 0 {
			if err == nil {
				err = errors.New("must not be negative")
			}
			return nil, fmt.Errorf("min: %w", err)
		}
		r.Min = f
	}
	if v := get("for"); v != "" {
		d, err := ParseSpan(v)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("for must be a time such as 5m, got %q", v)
		}
		r.For = d
	}
	if v := get("direction"); v != "" {
		if v != "up" && v != "down" && v != "both" {
			return nil, fmt.Errorf("direction must be up, down or both, got %q", v)
		}
		r.Direction = v
	}
	if (r.Compare > 0 || r.Min > 0) && r.Change == 0 {
		return nil, errors.New("compare and min apply to change, which is not set")
	}
	if get("direction") != "" && r.Change == 0 {
		return nil, errors.New("direction applies to change, which is not set")
	}
	if r.For > 0 && !r.Active() {
		return nil, errors.New("for applies to warn, crit or change, and none is set")
	}
	// A value cannot fall by more than all of itself.
	if r.Change > 1 && r.Direction == "down" {
		return nil, fmt.Errorf("change %s with direction=down can never happen: a value falls by 100%% at most", get("change"))
	}
	return r, nil
}

// checkBounds refuses limits that cannot work together. On the same side
// the severe one must lie further out, or the other can never show. On
// opposite sides there must be a value between them that is fine.
func checkBounds(w, c *Bound, ws, cs string) error {
	if w == nil || c == nil {
		return nil
	}
	if w.Below == c.Below {
		side := "above"
		if w.Below {
			side = "below"
		}
		if (!w.Below && c.X < w.X) || (w.Below && c.X > w.X) {
			return fmt.Errorf("crit must be further %s than warn (warn %s, crit %s)", side, ws, cs)
		}
		// The same number: warn only shows when it includes the number and
		// crit does not.
		if c.X == w.X && !(w.Incl && !c.Incl) {
			return fmt.Errorf("warn can never show: crit is reached at the same value (warn %s, crit %s)", ws, cs)
		}
		return nil
	}
	low, high := w, c // low: breached below it; high: breached above it
	if c.Below {
		low, high = c, w
	}
	if high.X < low.X || (high.X == low.X && (high.Incl || low.Incl)) {
		return fmt.Errorf("no value would be fine: warn %s and crit %s leave nothing between them", ws, cs)
	}
	return nil
}

// isCondition reports whether a limit is written as a condition (it holds
// the word and, or or).
func isCondition(v string) bool {
	return splitJoiner(v) != nil || strings.Contains(strings.ToLower(v), " and ") || strings.Contains(strings.ToLower(v), " or ")
}

// splitJoiner cuts v at every " and " or " or " (any case), giving the
// parts and the joiners found.
func splitJoiner(v string) []string {
	parts := []string{}
	rest := v
	for {
		lo := strings.ToLower(rest)
		ia, io := strings.Index(lo, " and "), strings.Index(lo, " or ")
		i, w := ia, 5
		if ia < 0 || (io >= 0 && io < ia) {
			i, w = io, 4
		}
		if i < 0 {
			parts = append(parts, strings.TrimSpace(rest))
			break
		}
		parts = append(parts, strings.TrimSpace(rest[:i]), strings.ToLower(strings.TrimSpace(rest[i:i+w])))
		rest = rest[i+w:]
	}
	if len(parts) == 1 {
		return nil
	}
	return parts
}

// parseCond reads "busy>50 and queue>=100": two to MaxTerms comparisons,
// one joiner throughout, each naming a field of the check, compared with
// <, <=, > or >= to a limit in that field's unit.
func parseCond(v string, fields Fields) (*Cond, error) {
	if fields == nil {
		return nil, errors.New("a condition names fields of an application page, and this check has none")
	}
	parts := splitJoiner(v)
	if parts == nil {
		return nil, fmt.Errorf("want a condition such as busy>50 and queue>100, got %q", v)
	}
	c := &Cond{Text: strings.Join(strings.Fields(v), " ")}
	for i, p := range parts {
		if i%2 == 1 {
			or := p == "or"
			if i == 1 {
				c.Or = or
			} else if or != c.Or {
				return nil, errors.New("use only and, or only or, in one condition: mixing them has no single meaning without brackets")
			}
			continue
		}
		t, err := parseTerm(p, fields)
		if err != nil {
			return nil, err
		}
		c.Terms = append(c.Terms, t)
	}
	if len(c.Terms) > MaxTerms {
		return nil, fmt.Errorf("a condition holds at most %d comparisons", MaxTerms)
	}
	seen := map[string]bool{}
	for _, t := range c.Terms {
		if seen[t.Name] {
			return nil, fmt.Errorf("%s is compared twice: write one limit per field", t.Name)
		}
		seen[t.Name] = true
	}
	return c, nil
}

func parseTerm(p string, fields Fields) (Term, error) {
	i := strings.IndexAny(p, "<>=!")
	if i <= 0 {
		return Term{}, fmt.Errorf("want field<limit or field>limit, got %q", p)
	}
	name, rest := strings.TrimSpace(p[:i]), strings.TrimSpace(p[i:])
	f, ok := fields(name)
	if !ok {
		return Term{}, fmt.Errorf("%q is not a field of this check", name)
	}
	if !strings.HasPrefix(rest, "<") && !strings.HasPrefix(rest, ">") {
		return Term{}, fmt.Errorf("%q: compare with <, <=, > or >=", p)
	}
	b, err := parseBound(rest, f.Unit)
	if err != nil {
		return Term{}, fmt.Errorf("%s: %w", name, err)
	}
	if b == nil {
		return Term{}, fmt.Errorf("%q has no limit", p)
	}
	return Term{Name: name, Field: f, Bound: *b}, nil
}

func parseBound(v, unit string) (*Bound, error) {
	if v == "" {
		return nil, nil
	}
	b := &Bound{}
	switch {
	case strings.HasPrefix(v, "<="):
		b.Below, b.Incl, v = true, true, v[2:]
	case strings.HasPrefix(v, ">="):
		b.Incl, v = true, v[2:]
	case strings.HasPrefix(v, "<"):
		b.Below, v = true, v[1:]
	default:
		v = strings.TrimPrefix(v, ">")
	}
	f, err := ParseQuantity(v, unit)
	if err != nil {
		return nil, err
	}
	b.X = f
	if unit == "%" && f > 100 {
		return nil, fmt.Errorf("%q can never be reached: a percentage does not exceed 100", v)
	}
	if b.Below && f == 0 && !b.Incl && NeverNegative(unit) {
		return nil, fmt.Errorf("%q can never be reached: this value is never below zero", "<"+v)
	}
	return b, nil
}

// ParseQuantity reads a limit for a value measured in unit: "bps", "%",
// "°C", "B" (bytes), "s" (seconds), "1" (a plain count or ratio), or ""
// when what the value measures is not known. A unit written with the
// number must measure the same thing as the value: 500Mbps is not a
// percentage and 80% is not a count, which would otherwise be converted
// quietly into a limit that can never be reached. A limit below zero is
// refused for a value that is never negative.
func ParseQuantity(v, unit string) (float64, error) {
	want := unitKind[unit]
	if want == "time" {
		// m is minutes here, never mega: 5m on a time value is 300 s.
		return parseSeconds(v)
	}
	f, err := ParseNumber(v)
	if err != nil {
		return 0, err
	}
	if want == "" {
		return f, nil
	}
	i := 0
	t := strings.TrimSpace(v)
	for i < len(t) && (t[i] == '-' || t[i] == '+' || t[i] == '.' || (t[i] >= '0' && t[i] <= '9') || t[i] == 'e' && i > 0 && i+1 < len(t) && (t[i+1] >= '0' && t[i+1] <= '9' || t[i+1] == '-')) {
		i++
	}
	suffix := strings.ToLower(strings.TrimSpace(t[i:]))
	got := unitClass[suffix]
	if got != "plain" && got != "scale" && got != want {
		return 0, fmt.Errorf("%q is %s, but this value is %s", v, article(got), article(want))
	}
	if want == "temperature" && f < -273.15 {
		return 0, fmt.Errorf("%q is below absolute zero", v)
	}
	if f < 0 && want != "temperature" {
		return 0, fmt.Errorf("%q is below zero, which this value never is", v)
	}
	return f, nil
}

// unitKind says what a value's unit measures.
var unitKind = map[string]string{"bps": "rate", "%": "percent", "°C": "temperature", "B": "size", "s": "time", "1": "count", "/s": "count"}

// NeverNegative reports whether a value in unit is never below zero.
func NeverNegative(unit string) bool { k := unitKind[unit]; return k != "" && k != "temperature" }

func article(class string) string {
	switch class {
	case "":
		return "another kind of quantity"
	case "count":
		return "a plain number"
	}
	return "a " + class
}

// unitClass says what each unit measures.
var unitClass = map[string]string{
	"": "plain", "%": "percent", "°c": "temperature", "c": "temperature", "℃": "temperature",
	"ms": "time", "s": "time",
	"bps": "rate", "kbps": "rate", "mbps": "rate", "gbps": "rate", "tbps": "rate",
	"b": "size", "kb": "size", "mb": "size", "gb": "size", "tb": "size",
	"k": "scale", "m": "scale", "g": "scale",
}

// ParseSpan reads a length of time: Go's durations plus days, so 1d12h is
// 36 hours (not 13, nor an error).
func ParseSpan(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	var days time.Duration
	if i := strings.IndexByte(v, 'd'); i > 0 {
		n, err := strconv.Atoi(v[:i])
		if err != nil {
			return 0, fmt.Errorf("want a time such as 90s, 5m, 2h or 1d12h, got %q", v)
		}
		days, v = time.Duration(n)*24*time.Hour, v[i+1:]
		if v == "" {
			return days, nil
		}
		if v[0] == '-' || v[0] == '+' {
			// 1d-12h: one sign for the whole time, not a sum of signed parts.
			return 0, fmt.Errorf("want a time such as 90s, 5m, 2h or 1d12h, got %q", v)
		}
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("want a time such as 90s, 5m, 2h or 1d12h, got %q", v)
	}
	return days + d, nil
}

// parseSeconds reads a limit for a value in seconds: a plain number is
// seconds; ms, s, m (minutes), h and d are times.
func parseSeconds(v string) (float64, error) {
	t := strings.TrimSpace(v)
	if f, err := strconv.ParseFloat(t, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return 0, fmt.Errorf("want a time such as 300, 500ms, 5m or 2h, got %q", v)
		}
		return f, nil
	}
	if strings.HasSuffix(t, "ms") && !strings.ContainsAny(t[:len(t)-2], "hdms") {
		f, err := strconv.ParseFloat(t[:len(t)-2], 64)
		if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("want a time such as 300, 500ms, 5m or 2h, got %q", v)
		}
		return f / 1000, nil
	}
	d, err := ParseSpan(t)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q is not a time: write 300 (seconds), 500ms, 5m or 2h", v)
	}
	return d.Seconds(), nil
}

// ParseNumber reads a number with an optional unit: 80, 80%, 1.5GB,
// 100Mbps, 35°C. Rates are in bits per second, sizes in bytes.
func ParseNumber(v string) (float64, error) {
	v = strings.TrimSpace(v)
	i := 0
	for i < len(v) && (v[i] == '-' || v[i] == '+' || v[i] == '.' || (v[i] >= '0' && v[i] <= '9') || v[i] == 'e' && i > 0 && i+1 < len(v) && (v[i+1] >= '0' && v[i+1] <= '9' || v[i+1] == '-')) {
		i++
	}
	f, err := strconv.ParseFloat(v[:i], 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("want a number, got %q", v)
	}
	raw := strings.TrimSpace(v[i:])
	// Gb, Mb, kb, b: bits or bytes? Network people mean bits, the old
	// reading was bytes. Refused rather than guessed: 1GB is bytes, 1Gbps a rate.
	if len(raw) > 0 && raw[len(raw)-1] == 'b' && (len(raw) == 1 || raw[len(raw)-2] != 'p') {
		return 0, fmt.Errorf("%q: write B for bytes (1GB) or bps for a rate (1Gbps); a small b is ambiguous", v)
	}
	unit := strings.ToLower(raw)
	mul, ok := units[unit]
	if !ok {
		return 0, fmt.Errorf("unknown unit %q", v[i:])
	}
	return f * mul, nil
}

var units = map[string]float64{
	"": 1, "%": 1, "°c": 1, "c": 1, "℃": 1, "ms": 1, "s": 1000,
	"bps": 1, "kbps": 1e3, "mbps": 1e6, "gbps": 1e9, "tbps": 1e12,
	"b": 1, "kb": 1 << 10, "mb": 1 << 20, "gb": 1 << 30, "tb": 1 << 40,
	"k": 1e3, "m": 1e6, "g": 1e9,
}

// Format writes v with its unit in a readable size: 9.2 Gbps, 41 °C.
func Format(v float64, unit string) string {
	num := func(x float64) string {
		s := strconv.FormatFloat(x, 'f', 2, 64)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		if s == "-0" {
			s = "0"
		}
		return s
	}
	switch unit {
	case "bps":
		for _, u := range []struct {
			n string
			f float64
		}{{"Tbps", 1e12}, {"Gbps", 1e9}, {"Mbps", 1e6}, {"Kbps", 1e3}} {
			if math.Abs(v) >= u.f {
				return num(v/u.f) + " " + u.n
			}
		}
		return num(v) + " bps"
	case "%":
		return num(v) + "%"
	case "/s":
		return num(v) + "/s"
	case "", "1":
		return num(v)
	case "B":
		for _, u := range []struct {
			n string
			f float64
		}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}} {
			if math.Abs(v) >= u.f {
				return num(v/u.f) + " " + u.n
			}
		}
		return num(v) + " B"
	}
	return num(v) + " " + unit
}

// Sample is one judged value.
type Sample struct {
	At time.Time
	V  float64
}

// Tracker holds what one monitor's rules need between rounds.
type Tracker struct {
	Rules   *Rules
	Retain  time.Duration // how long samples are kept
	Window  time.Duration // the reference is the median over this window
	samples []Sample
	// counters: the last raw reading of each counter, for rates.
	counters map[string]Sample
	since    time.Time          // the current breach started
	speed    float64            // the port speed last seen
	termVals map[string]float64 // this round's number of each field a condition names
}

// Load gives the tracker samples kept from before (a restart).
func (t *Tracker) Load(s []Sample) { t.samples = append(t.samples[:0], s...) }

// Samples returns the kept samples, oldest first.
func (t *Tracker) Samples() []Sample { return append([]Sample(nil), t.samples...) }

// Outcome is one judgement.
type Outcome struct {
	Level  Level
	Value  float64
	Ref    float64 // the reference compared against (0 when none)
	HasRef bool
	// Detail says what was found and why, in English: "in 9.2 Gbps, 124%
	// above 4.1 Gbps 24h ago (limit 50%)".
	Detail string
	// LearningLeft is how long until a reference exists.
	LearningLeft time.Duration
	// Sampled reports whether Value was recorded (a new sample to keep).
	Sampled bool
}

// Observe takes one round's numbers (the check's metrics) and judges them.
func (t *Tracker) Observe(now time.Time, m map[string]float64) Outcome {
	t.readTerms(now, m) // before anything can return: a counter's rate needs every reading
	v, ok, note := t.value(now, m)
	if !ok {
		if note == noValue {
			return Outcome{Level: Missing, Detail: "the check reported no value for these rules"}
		}
		return Outcome{Level: Skip, Detail: note}
	}
	out := t.judge(now, v)
	out.Value, out.Sampled = v, true
	t.samples = append(t.samples, Sample{now, v})
	t.prune(now)
	if note != "" && out.Level < Warn {
		out.Level, out.Detail = Warn, note
	}
	return out
}

// value works out the number judged this round from the metrics.
func (t *Tracker) value(now time.Time, m map[string]float64) (float64, bool, string) {
	if t.counters == nil {
		t.counters = map[string]Sample{}
	}
	rate := func(key string) (float64, bool) {
		raw, ok := m[key]
		if !ok {
			return 0, false
		}
		prev, had := t.counters[key]
		t.counters[key] = Sample{now, raw}
		dt := now.Sub(prev.At).Seconds()
		if !had || dt <= 0 || raw < prev.V {
			return 0, false // first reading, or wrapped / restarted: one sample lost
		}
		return (raw - prev.V) / dt, true
	}
	note := ""
	if sp, ok := m["if.speed_bps"]; ok {
		if t.speed > 0 && sp > 0 && sp < t.speed {
			note = fmt.Sprintf("link speed fell from %s to %s", Format(t.speed, "bps"), Format(sp, "bps"))
		}
		t.speed = sp
	}
	switch t.Rules.Value {
	case "in", "out", "util":
		if _, ok := m["if.in_octets"]; !ok {
			return 0, false, noValue // not a port check
		}
		in, ok1 := rate("if.in_octets")
		out, ok2 := rate("if.out_octets")
		if !ok1 || !ok2 {
			return 0, false, ""
		}
		in, out = in*8, out*8
		switch t.Rules.Value {
		case "in":
			return in, true, note
		case "out":
			return out, true, note
		}
		if t.speed <= 0 {
			return 0, false, ""
		}
		return math.Max(in, out) / t.speed * 100, true, note
	}
	v, ok := m["value"]
	if !ok {
		return 0, false, noValue
	}
	if m["value.counter"] == 1 {
		// A counter only ever grows: its rate is what means something.
		r, ok := rate("value")
		if !ok {
			return 0, false, ""
		}
		if t.Rules.Unit == "" || t.Rules.Unit == "1" {
			t.Rules.Unit = "/s"
		}
		return r, true, note
	}
	return v, ok, note
}

// noValue marks a round whose metrics hold nothing the rules can judge.
const noValue = "\x00no value"

func (t *Tracker) judge(now time.Time, v float64) Outcome {
	r := t.Rules
	out := Outcome{Level: OK}
	level, why := OK, ""
	if why = t.condWhy(r.CritCond); why != "" {
		level = Crit
	} else if why = t.condWhy(r.WarnCond); why != "" {
		level = Warn
	}
	if level == Crit {
		// a condition already decided the worst
	} else if r.Crit != nil && r.Crit.breached(v) {
		level, why = Crit, fmt.Sprintf("%s, limit %s%s", Format(v, r.Unit), side(r.Crit), Format(r.Crit.X, r.Unit))
	} else if r.Warn != nil && r.Warn.breached(v) {
		level, why = Warn, fmt.Sprintf("%s, limit %s%s", Format(v, r.Unit), side(r.Warn), Format(r.Warn.X, r.Unit))
	}
	if r.Change > 0 {
		ref, ok, left := t.reference(now)
		out.Ref, out.HasRef = ref, ok
		switch {
		case !ok:
			if level == OK {
				out.Level, out.LearningLeft = Learning, left
				out.Detail = fmt.Sprintf("%s; learning, a reference in %s", Format(v, r.Unit), left.Round(time.Minute))
				return out
			}
		case math.Abs(v) < r.Min && math.Abs(ref) < r.Min:
			// Both small: an idle port going from 1 to 5 is no news.
		case ref != 0:
			ch := (v - ref) / math.Abs(ref)
			up := ch > 0
			if math.Abs(ch) > r.Change && (r.Direction == "both" || (r.Direction == "up") == up) && level < Crit {
				dir := "above"
				if !up {
					dir = "below"
				}
				level, why = Warn, fmt.Sprintf("%s, %.0f%% %s %s %s (limit %.0f%%)", Format(v, r.Unit), math.Abs(ch)*100, dir,
					Format(ref, r.Unit), t.refName(), r.Change*100)
			}
		}
		if ok && v == 0 && ref > r.Min && ref > 0 && level < Warn && r.Direction != "up" {
			level, why = Warn, fmt.Sprintf("fell to 0 from %s %s", Format(ref, r.Unit), t.refName())
		}
		// From nothing to something has no percentage, but it is a change.
		if ok && ref == 0 && v > r.Min && v > 0 && level < Warn && r.Direction != "down" {
			level, why = Warn, fmt.Sprintf("%s, up from 0 %s", Format(v, r.Unit), t.refName())
		}
	}
	if level == OK {
		t.since = time.Time{}
		out.Detail = Format(v, r.Unit)
		return out
	}
	if t.since.IsZero() {
		t.since = now
	}
	if now.Sub(t.since) < r.For {
		out.Detail = fmt.Sprintf("%s; for %s of %s", why, now.Sub(t.since).Round(time.Second), r.For)
		return out
	}
	out.Level, out.Detail = level, why
	return out
}

// readTerms takes this round's number of each field a condition names;
// a counter's number is its rate, so a field that is missing, or a counter
// on its first reading, has none.
func (t *Tracker) readTerms(now time.Time, m map[string]float64) {
	t.termVals = map[string]float64{}
	if t.Rules == nil {
		return
	}
	if t.counters == nil {
		t.counters = map[string]Sample{}
	}
	for _, c := range []*Cond{t.Rules.WarnCond, t.Rules.CritCond} {
		if c == nil {
			continue
		}
		for _, tm := range c.Terms {
			if _, done := t.termVals[tm.Name]; done {
				continue
			}
			key := "app." + tm.Name
			raw, ok := m[key]
			if !ok {
				continue
			}
			if !tm.Field.Counter {
				t.termVals[tm.Name] = raw
				continue
			}
			prev, had := t.counters["cond:"+tm.Name]
			t.counters["cond:"+tm.Name] = Sample{now, raw}
			if dt := now.Sub(prev.At).Seconds(); had && dt > 0 && raw >= prev.V {
				t.termVals[tm.Name] = (raw - prev.V) / dt
			}
		}
	}
}

// condWhy says why a condition holds ("busy 60 >50 and queue 120 >100"),
// or "" when it does not. A comparison with no number this round does not
// hold, so an "and" cannot be confirmed and an "or" rests on the others.
func (t *Tracker) condWhy(c *Cond) string {
	if c == nil {
		return ""
	}
	var held []string
	for _, tm := range c.Terms {
		v, ok := t.termVals[tm.Name]
		if ok && tm.Bound.breached(v) {
			unit := tm.Field.Unit
			if tm.Field.Counter && (unit == "" || unit == "1") {
				unit = "/s"
			}
			held = append(held, fmt.Sprintf("%s %s %s%s", tm.Name, Format(v, unit), side(&tm.Bound), Format(tm.Bound.X, unit)))
		}
	}
	if (c.Or && len(held) > 0) || (!c.Or && len(held) == len(c.Terms)) {
		joiner := " and "
		if c.Or {
			joiner = " or "
		}
		return strings.Join(held, joiner)
	}
	return ""
}

func side(b *Bound) string {
	s := ">"
	if b.Below {
		s = "<"
	}
	if b.Incl {
		s += "="
	}
	return s
}

func (t *Tracker) refName() string {
	if t.Rules.Compare == 0 {
		return "the sample before"
	}
	return t.Rules.Compare.String() + " ago"
}

// reference is the value compared against: the previous sample, or the
// median of the samples around the same time Compare earlier.
func (t *Tracker) reference(now time.Time) (float64, bool, time.Duration) {
	if t.Rules.Compare == 0 {
		if len(t.samples) == 0 {
			return 0, false, 0
		}
		return t.samples[len(t.samples)-1].V, true, 0
	}
	at := now.Add(-t.Rules.Compare)
	half := t.Window / 2
	var vals []float64
	for _, s := range t.samples {
		if !s.At.Before(at.Add(-half)) && !s.At.After(at.Add(half)) {
			vals = append(vals, s.V)
		}
	}
	if len(vals) == 0 {
		left := t.Rules.Compare
		if len(t.samples) > 0 {
			left = t.samples[0].At.Add(t.Rules.Compare).Sub(now)
		}
		return 0, false, max(left, 0)
	}
	sort.Float64s(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 0 {
		return (vals[mid-1] + vals[mid]) / 2, true, 0
	}
	return vals[mid], true, 0
}

func (t *Tracker) prune(now time.Time) {
	keep := t.Retain
	if t.Rules.Compare+t.Window > keep {
		keep = t.Rules.Compare + t.Window
	}
	i := 0
	for i < len(t.samples) && now.Sub(t.samples[i].At) > keep {
		i++
	}
	t.samples = t.samples[i:]
}
