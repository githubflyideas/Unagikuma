package schedule

import (
	"testing"
	"time"
)

func TestPeriod(t *testing.T) {
	loc := time.UTC
	at := func(s string) time.Time { x, _ := time.ParseInLocation("2006-01-02 15:04", s, loc); return x }
	work, err := ParsePeriod("mon-fri 09:00-18:00; sat 10:00-12:00")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"2026-10-26 09:00": true,  // Monday
		"2026-10-26 17:59": true,  // Monday
		"2026-10-26 18:00": false, // end is exclusive
		"2026-10-31 11:00": true,  // Saturday
		"2026-10-31 13:00": false,
		"2026-11-01 10:00": false, // Sunday
	}
	for s, want := range cases {
		if got := work.Contains(at(s), loc); got != want {
			t.Errorf("work %s: %v", s, got)
		}
	}
	night, err := ParsePeriod("fri 22:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	for s, want := range map[string]bool{"2026-10-30 23:00": true, "2026-10-31 05:59": true, "2026-10-31 06:00": false, "2026-10-29 23:00": false, "2026-10-30 05:00": false} {
		if got := night.Contains(at(s), loc); got != want {
			t.Errorf("night %s: %v", s, got)
		}
	}
	all, _ := ParsePeriod("day")
	if !all.Contains(at("2026-11-01 03:00"), loc) {
		t.Error("whole day")
	}
	full, _ := ParsePeriod("sun 00:00-24:00")
	if !full.Contains(at("2026-11-01 23:59"), loc) || full.Contains(at("2026-11-02 00:00"), loc) {
		t.Error("24:00 end")
	}
	for _, bad := range []string{"", "mon 25:00-26:00", "month:1 09:00-10:00", "mon 09:00-09:00", "funday 09:00-10:00", "mon 0900"} {
		if _, err := ParsePeriod(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
