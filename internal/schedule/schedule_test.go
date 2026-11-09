package schedule

import (
	"testing"
	"time"
)

var loc = time.FixedZone("CST", 8*3600)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWeeklyWindow(t *testing.T) {
	var sp Spec
	if err := ParseEvery("sun", &sp); err != nil {
		t.Fatal(err)
	}
	sp.Start, _ = ParseClock("23:00")
	sp.Duration = 3 * time.Hour
	// 2026-11-01 is a Sunday; the window runs into Monday.
	for when, want := range map[string]bool{
		"2026-11-01 22:59": false, "2026-11-01 23:00": true, "2026-11-02 01:59": true,
		"2026-11-02 02:00": false, "2026-11-03 23:30": false,
	} {
		if _, _, ok := sp.Active(at(when), loc); ok != want {
			t.Errorf("%s: active=%v", when, ok)
		}
	}
	next, _ := sp.Next(at("2026-11-02 08:00"), loc)
	if !next.Equal(at("2026-11-08 23:00")) {
		t.Fatalf("next %v", next)
	}
}

func TestEveryForms(t *testing.T) {
	var sp Spec
	if err := ParseEvery("mon-fri", &sp); err != nil || !sp.Weekdays[time.Friday] || sp.Weekdays[time.Saturday] {
		t.Fatalf("mon-fri: %v %v", err, sp.Weekdays)
	}
	sp = Spec{}
	if err := ParseEvery("fri-mon", &sp); err != nil || !sp.Weekdays[time.Sunday] || sp.Weekdays[time.Wednesday] {
		t.Fatalf("fri-mon: %v", sp.Weekdays)
	}
	sp = Spec{}
	if err := ParseEvery("month:1,15", &sp); err != nil || !sp.MonthDays[15] || sp.MonthDays[2] {
		t.Fatal(err)
	}
	for _, bad := range []string{"someday", "month:32", "mon-xyz"} {
		if err := ParseEvery(bad, &Spec{}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseClock("24:00"); err == nil {
		t.Error("24:00 accepted")
	}
}

func TestOneOff(t *testing.T) {
	start, err := ParseAt("2026-11-01 22:00", loc)
	if err != nil {
		t.Fatal(err)
	}
	sp := Spec{At: start, Duration: time.Hour}
	if _, _, ok := sp.Active(at("2026-11-01 22:30"), loc); !ok {
		t.Fatal("inside")
	}
	if _, _, ok := sp.Active(at("2026-11-01 23:00"), loc); ok {
		t.Fatal("after")
	}
	if _, ok := sp.Next(at("2026-11-02 00:00"), loc); ok {
		t.Fatal("a past one-off window has no next")
	}
}

func TestCron(t *testing.T) {
	c, err := ParseCron("30 2 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	sp := Spec{Cron: c, Duration: 90 * time.Minute}
	if _, _, ok := sp.Active(at("2026-11-02 03:59"), loc); !ok { // Monday
		t.Fatal("monday")
	}
	if _, _, ok := sp.Active(at("2026-11-01 03:00"), loc); ok { // Sunday
		t.Fatal("sunday")
	}
	next, _ := c.Next(at("2026-11-06 03:00")) // Friday after the window
	if !next.Equal(at("2026-11-09 02:30")) {
		t.Fatalf("next %v", next)
	}
	c2, _ := ParseCron("*/15 * 1 * 0") // every 15 min on the 1st or on Sundays
	if !c2.Matches(at("2026-11-01 10:45")) || !c2.Matches(at("2026-12-01 10:00")) || c2.Matches(at("2026-12-02 10:00")) {
		t.Fatal("day fields are OR-ed when both are set")
	}
	if c3, err := ParseCron("0 0 * * 5-7"); err != nil || !c3.Matches(at("2026-11-01 00:00")) || !c3.Matches(at("2026-11-06 00:00")) {
		t.Fatalf("5-7 covers Friday to Sunday: %v", err)
	}
	for _, bad := range []string{"* * * *", "60 * * * *", "* * 0 * *", "*/0 * * * *", "a * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
