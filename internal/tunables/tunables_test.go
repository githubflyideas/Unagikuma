package tunables

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	var s Set
	if s.Int("notify.max") != 3 || s.Duration("notify.repeat") != 30*time.Minute {
		t.Fatal("notify defaults")
	}
	if h, m := s.TimeOfDay("notify.daily-reminder-at"); h != 10 || m != 0 {
		t.Fatal("reminder default")
	}
	if !s.Bool("localnet.enabled") || s.Int("localnet.min-domains") != 3 {
		t.Fatal("localnet defaults")
	}
	if r := s.Rate("notify.channel-rate"); r < 0.33 || r > 0.34 {
		t.Fatalf("rate %v", r)
	}
	if s.Duration("retention.agg1h") != 400*24*time.Hour {
		t.Fatal("day durations")
	}
}

func TestOverrides(t *testing.T) {
	s, err := New(map[string]string{"notify.max": "5", "localnet.min-domains": "4"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Int("notify.max") != 5 || s.Int("localnet.min-domains") != 4 {
		t.Fatal("override not applied")
	}
	bad := []map[string]string{
		{"notify.max": "-1"},
		{"notify.max": "x"},
		{"localnet.exit-ratio": "1.5"},
		{"nope": "1"},
		{"notify.daily-reminder-at": "25:00"},
		{"notify.repeat": "10s"},
		{"api.rate": "fast"},
	}
	for _, b := range bad {
		if _, err := New(b); err == nil {
			t.Errorf("expected error for %v", b)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{"30": 30 * time.Second, "1d12h": 36 * time.Hour, "200ms": 200 * time.Millisecond, "7d": 7 * 24 * time.Hour}
	for in, want := range cases {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
}

func TestRejectsNonsense(t *testing.T) {
	for _, s := range []string{"-1d", "-5", "-1s", "99999999999d", "106752d", "9223372037s", "1d9223372036854775807ns"} {
		if _, err := ParseDuration(s); err == nil {
			t.Errorf("ParseDuration accepted %q", s)
		}
	}
	for _, s := range []string{"NaN/s", "Inf/m", "-1/s", "0/s"} {
		if _, err := ParseRate(s); err == nil {
			t.Errorf("ParseRate accepted %q", s)
		}
	}
	for _, v := range []string{"8.8.8", "dns.google", "8.8.8.8:53"} {
		if _, err := New(map[string]string{"localnet.exit-target": v}); err == nil {
			t.Errorf("address tunable accepted %q", v)
		}
	}
	for _, v := range []string{"", "8.8.8.8", "2001:4860:4860::8888"} {
		if _, err := New(map[string]string{"localnet.exit-target": v}); err != nil {
			t.Errorf("address tunable refused %q: %v", v, err)
		}
	}
	for _, v := range []string{"NaN", "Inf", "-Inf", "nan"} {
		if _, err := New(map[string]string{"localnet.exit-ratio": v}); err == nil {
			t.Errorf("float tunable accepted %q", v)
		}
	}
}
