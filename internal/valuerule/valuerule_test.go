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
