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
}

func (b Bound) breached(v float64) bool {
	if b.Below {
		return v < b.X
	}
	return v > b.X
}

// Rules are the options of one monitor.
type Rules struct {
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
	return r != nil && (r.Warn != nil || r.Crit != nil || r.Change > 0)
}

// Parse reads the rules from a monitor's options (get returns "" for an
// option that is not set). unit is the unit of the check's own value.
func Parse(get func(string) string, unit string) (*Rules, error) {
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
	if r.Warn, err = parseBound(get("warn")); err != nil {
		return nil, fmt.Errorf("warn: %w", err)
	}
	if r.Crit, err = parseBound(get("crit")); err != nil {
		return nil, fmt.Errorf("crit: %w", err)
	}
	if v := get("change"); v != "" {
		f, err := ParseNumber(strings.TrimSuffix(v, "%"))
		if err != nil || f <= 0 {
			return nil, fmt.Errorf("change must be a percentage such as 50%%, got %q", v)
		}
		r.Change = f / 100
	}
	if v := get("compare"); v != "" && v != "previous" {
		d, err := time.ParseDuration(strings.Replace(v, "d", "h", 1))
		if strings.HasSuffix(v, "d") {
			n, e := strconv.Atoi(strings.TrimSuffix(v, "d"))
			d, err = time.Duration(n)*24*time.Hour, e
		}
		if err != nil || d < 0 {
			return nil, fmt.Errorf("compare must be previous, or a time such as 24h, got %q", v)
		}
		if d > time.Minute { // a minute or less is the previous sample
			r.Compare = d
		}
	}
	if v := get("min"); v != "" {
		f, err := ParseNumber(v)
		if err != nil || f < 0 {
			return nil, fmt.Errorf("min must be a number, got %q", v)
		}
		r.Min = f
	}
	if v := get("for"); v != "" {
		d, err := time.ParseDuration(v)
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
	return r, nil
}

func parseBound(v string) (*Bound, error) {
	if v == "" {
		return nil, nil
	}
	b := &Bound{}
	if strings.HasPrefix(v, "<") {
		b.Below, v = true, v[1:]
	} else {
		v = strings.TrimPrefix(v, ">")
	}
	f, err := ParseNumber(v)
	if err != nil {
		return nil, err
	}
	b.X = f
	return b, nil
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
	unit := strings.ToLower(strings.TrimSpace(v[i:]))
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
	case "":
		return num(v)
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
	since    time.Time // the current breach started
	speed    float64   // the port speed last seen
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
	return v, ok, note
}

// noValue marks a round whose metrics hold nothing the rules can judge.
const noValue = "\x00no value"

func (t *Tracker) judge(now time.Time, v float64) Outcome {
	r := t.Rules
	out := Outcome{Level: OK}
	level, why := OK, ""
	if r.Crit != nil && r.Crit.breached(v) {
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
		case v < r.Min && ref < r.Min:
			// Both small: an idle port going from 1 to 5 is no news.
		case ref > 0:
			ch := (v - ref) / ref
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

func side(b *Bound) string {
	if b.Below {
		return "<"
	}
	return ">"
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
