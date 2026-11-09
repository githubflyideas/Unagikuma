package oncall

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, opts map[string]string, members ...string) *Rotation {
	t.Helper()
	r, err := Parse(opts, members)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParse(t *testing.T) {
	if r, err := Parse(map[string]string{}, []string{"a"}); r != nil || err != nil {
		t.Fatalf("no rotate=: %v %v", r, err)
	}
	for _, c := range []struct {
		opts map[string]string
		err  string
	}{
		{map[string]string{"handoff": "09:00"}, "needs rotate="},
		{map[string]string{"rotate": "monthly"}, "rotate must be"},
		{map[string]string{"rotate": "0d"}, "rotate must be"},
		{map[string]string{"rotate": "91d"}, "rotate must be"},
		{map[string]string{"rotate": "daily", "handoff": "mon 09:00"}, "whole weeks"},
		{map[string]string{"rotate": "weekly", "handoff": "xyz 09:00"}, "unknown day"},
		{map[string]string{"rotate": "weekly", "handoff": "mon 9am"}, "HH:MM"},
		{map[string]string{"rotate": "weekly", "start": "next week"}, "start must be"},
	} {
		if _, err := Parse(c.opts, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%v: %v, want %q", c.opts, err, c.err)
		}
	}
	r := mustParse(t, map[string]string{"rotate": "weekly"}, "a")
	if r.Handoff != "mon 09:00" || r.Weekday != time.Monday || r.Clock != 9*60 {
		t.Fatalf("weekly defaults: %+v", r)
	}
	if r := mustParse(t, map[string]string{"rotate": "12h"}, "a"); r.Hours != 12 || r.Handoff != "09:00" {
		t.Fatalf("hours: %+v", r)
	}
}

func TestWeekly(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	r := mustParse(t, map[string]string{"rotate": "weekly", "handoff": "mon 10:00", "start": "2026-11-02"}, "li", "wang", "zhao")
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04", s, loc); return v }
	for _, c := range []struct{ t, who, from string }{
		{"2026-11-02 10:00", "li", "2026-11-02 10:00"},
		{"2026-11-09 09:59", "li", "2026-11-02 10:00"},
		{"2026-11-09 10:00", "wang", "2026-11-09 10:00"},
		{"2026-11-20 03:00", "zhao", "2026-11-16 10:00"},
		{"2026-11-23 10:00", "li", "2026-11-23 10:00"},
		{"2026-11-01 12:00", "zhao", "2026-10-26 10:00"}, // before the start: backwards
	} {
		s := r.At(at(c.t), loc)
		if s.Member != c.who || !s.From.Equal(at(c.from)) || !s.Until.Equal(at(c.from).AddDate(0, 0, 7)) {
			t.Errorf("%s: %+v, want %s from %s", c.t, s, c.who, c.from)
		}
	}
	// A start that is not a handoff day: the first member takes the shift
	// running then.
	r2 := mustParse(t, map[string]string{"rotate": "weekly", "handoff": "mon 10:00", "start": "2026-11-04"}, "li", "wang")
	if s := r2.At(at("2026-11-04 12:00"), loc); s.Member != "li" || !s.From.Equal(at("2026-11-02 10:00")) {
		t.Fatalf("start mid-week: %+v", s)
	}
	if s := r2.At(at("2026-11-10 12:00"), loc); s.Member != "wang" {
		t.Fatalf("the week after: %+v", s)
	}
	next := r.Next(at("2026-11-10 12:00"), 4, loc)
	if len(next) != 4 || next[0].Member != "wang" || next[1].Member != "zhao" || next[3].Member != "wang" || !next[1].From.Equal(next[0].Until) {
		t.Fatalf("next: %+v", next)
	}
}

// Day shifts keep their local handoff time across a daylight saving change.
func TestDailyAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no time zone data")
	}
	r := mustParse(t, map[string]string{"rotate": "daily", "handoff": "08:00", "start": "2026-10-20"}, "a", "b")
	s := r.At(time.Date(2026, 10, 26, 12, 0, 0, 0, loc), loc) // the day after the change
	if s.From.Hour() != 8 || s.Until.Hour() != 8 || s.Member != "a" {
		t.Fatalf("across DST: %+v", s)
	}
}

func TestStandInsAndOrder(t *testing.T) {
	loc := time.UTC
	r := mustParse(t, map[string]string{"rotate": "daily", "handoff": "00:00", "start": "2026-11-01"}, "a", "b", "c")
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, loc) // b's day
	if got := strings.Join(r.Order("ops", nil, t0, loc), ","); got != "b,c,a" {
		t.Fatalf("order: %s", got)
	}
	ovs := []Override{
		{ID: 1, Rotation: "ops", Member: "c", From: t0.Add(-time.Hour), Until: t0.Add(time.Hour)},
		{ID: 2, Rotation: "ops", Member: "a", From: t0.Add(-time.Minute), Until: t0.Add(time.Minute)},
		{ID: 3, Rotation: "other", Member: "b", From: t0.Add(-time.Hour), Until: t0.Add(time.Hour)},
	}
	if o := Active("ops", ovs, t0); o == nil || o.ID != 2 {
		t.Fatalf("newest stand-in wins: %+v", o)
	}
	if got := strings.Join(r.Order("ops", ovs, t0, loc), ","); got != "a,b,c" {
		t.Fatalf("order with a stand-in: %s", got)
	}
	if o := Active("ops", ovs, t0.Add(time.Hour)); o != nil {
		t.Fatalf("the end is exclusive: %+v", o)
	}
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip("no time zone data")
	}
	return loc
}

// Hour shifts keep their local handoff times across a daylight saving
// change too: a 12h rotation handing off at 09:00 stays at 09:00 and 21:00.
func TestHourShiftsAcrossDST(t *testing.T) {
	ny := zone(t, "America/New_York")
	r := mustParse(t, map[string]string{"rotate": "12h", "handoff": "09:00", "start": "2026-10-30"}, "day", "night")
	for _, s := range r.Next(time.Date(2026, 10, 31, 10, 0, 0, 0, ny), 6, ny) {
		from, until := s.From.In(ny), s.Until.In(ny)
		if (from.Hour() != 9 && from.Hour() != 21) || (until.Hour() != 9 && until.Hour() != 21) || from.Minute() != 0 {
			t.Fatalf("12h shift drifted: %s %v – %v", s.Member, from, until)
		}
		if want := map[int]string{9: "day", 21: "night"}[from.Hour()]; s.Member != want {
			t.Fatalf("%v: %s, want %s", from, s.Member, want)
		}
	}
}

// The golden cases of the spec: a daily 09:00 handoff in New York lasts
// 25 hours across the autumn change and 23 across the spring one; London
// the same in October.
func TestDSTShiftLengths(t *testing.T) {
	for _, c := range []struct {
		zone string
		day  time.Time
		want time.Duration
	}{
		{"America/New_York", time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC), 25 * time.Hour},
		{"America/New_York", time.Date(2027, 3, 13, 18, 0, 0, 0, time.UTC), 23 * time.Hour},
		{"Europe/London", time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), 25 * time.Hour},
		{"Asia/Shanghai", time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), 24 * time.Hour},
	} {
		loc := zone(t, c.zone)
		r := mustParse(t, map[string]string{"rotate": "daily", "handoff": "09:00", "start": "2026-01-01"}, "a", "b")
		s := r.At(c.day, loc)
		if got := s.Until.Sub(s.From); got != c.want || s.From.In(loc).Hour() != 9 || s.Until.In(loc).Hour() != 9 {
			t.Errorf("%s %v: %v – %v (%v), want %v", c.zone, c.day, s.From.In(loc), s.Until.In(loc), got, c.want)
		}
	}
}

// A handoff time that does not exist (clocks jump forward over it) moves
// to the end of the gap; one that happens twice is the first of the two.
func TestHandoffInDSTGapAndOverlap(t *testing.T) {
	ny := zone(t, "America/New_York")
	r := mustParse(t, map[string]string{"rotate": "daily", "handoff": "02:30", "start": "2027-03-01"}, "a", "b")
	s := r.At(time.Date(2027, 3, 14, 12, 0, 0, 0, ny), ny)
	if want := time.Date(2027, 3, 14, 3, 0, 0, 0, ny); !s.From.Equal(want) {
		t.Fatalf("gap: shift starts %v, want %v", s.From.In(ny), want)
	}
	prev := r.At(s.From.Add(-time.Minute), ny)
	if !prev.Until.Equal(s.From) || prev.From.In(ny).Hour() != 2 || prev.From.In(ny).Minute() != 30 {
		t.Fatalf("gap: shift before %v – %v", prev.From.In(ny), prev.Until.In(ny))
	}
	r = mustParse(t, map[string]string{"rotate": "daily", "handoff": "01:30", "start": "2026-10-01"}, "a", "b")
	s = r.At(time.Date(2026, 11, 1, 12, 0, 0, 0, ny), ny)
	if _, off := s.From.Zone(); s.From.In(ny).Hour() != 1 || off != -4*3600 {
		t.Fatalf("overlap: shift starts %v, want the first 01:30 (EDT)", s.From.In(ny))
	}
}

// tz= makes the rotation hand off on its own clock whatever the server's
// time zone is.
func TestRotationTimeZone(t *testing.T) {
	sh := zone(t, "Asia/Shanghai")
	r := mustParse(t, map[string]string{"rotate": "daily", "handoff": "09:00", "start": "2026-11-01", "tz": "Asia/Shanghai"}, "a", "b")
	if r.TZ != "Asia/Shanghai" {
		t.Fatalf("tz: %+v", r)
	}
	for _, server := range []*time.Location{time.UTC, zone(t, "America/Los_Angeles")} {
		s := r.At(time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC), server) // 09:00 in Shanghai
		if s.Member != "b" || !s.From.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, sh)) {
			t.Fatalf("server in %v: %+v", server, s)
		}
	}
	for _, bad := range []string{"Mars/Base", "local", "+08:00"} {
		if _, err := Parse(map[string]string{"rotate": "daily", "tz": bad}, []string{"a"}); err == nil || !strings.Contains(err.Error(), "tz must be") {
			t.Errorf("tz=%s: %v", bad, err)
		}
	}
	if _, err := Parse(map[string]string{"tz": "UTC"}, []string{"a"}); err == nil {
		t.Error("tz without rotate= accepted")
	}
}

// The edges of the rotation order.
func TestRotationEdges(t *testing.T) {
	loc := time.UTC
	r := mustParse(t, map[string]string{"rotate": "daily", "handoff": "09:00", "start": "2026-11-01"}, "a", "b", "c")
	// Exactly at the handoff: the new shift (half-open intervals).
	if s := r.At(time.Date(2026, 11, 2, 9, 0, 0, 0, loc), loc); s.Member != "b" {
		t.Fatalf("at the handoff: %+v", s)
	}
	if s := r.At(time.Date(2026, 11, 2, 8, 59, 59, 0, loc), loc); s.Member != "a" {
		t.Fatalf("just before: %+v", s)
	}
	// Before the start the order runs backwards, so someone is on call.
	if s := r.At(time.Date(2026, 10, 31, 12, 0, 0, 0, loc), loc); s.Member != "c" {
		t.Fatalf("before the start: %+v", s)
	}
	// A weekly rotation starting mid-week: the first member's first week
	// is the one holding the start date.
	w := mustParse(t, map[string]string{"rotate": "weekly", "handoff": "mon 09:00", "start": "2026-11-04"}, "a", "b")
	if s := w.At(time.Date(2026, 11, 4, 12, 0, 0, 0, loc), loc); s.Member != "a" || !s.From.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, loc)) {
		t.Fatalf("mid-week start: %+v", s)
	}
	// A member listed twice takes two turns; the order still names each once.
	d := mustParse(t, map[string]string{"rotate": "daily", "handoff": "00:00", "start": "2026-11-01"}, "a", "b", "a", "c")
	var got []string
	for _, s := range d.Next(time.Date(2026, 11, 1, 1, 0, 0, 0, loc), 4, loc) {
		got = append(got, s.Member)
	}
	if strings.Join(got, ",") != "a,b,a,c" {
		t.Fatalf("repeated member: %v", got)
	}
	if o := d.Order("x", nil, time.Date(2026, 11, 2, 1, 0, 0, 0, loc), loc); strings.Join(o, ",") != "b,a,c" {
		t.Fatalf("order with a repeated member: %v", o)
	}
	// Overlapping stand-ins: the newest wins, and only inside its interval.
	os := []Override{
		{ID: 1, Rotation: "x", Member: "b", From: time.Date(2026, 11, 1, 0, 0, 0, 0, loc), Until: time.Date(2026, 11, 3, 0, 0, 0, 0, loc)},
		{ID: 2, Rotation: "x", Member: "c", From: time.Date(2026, 11, 2, 0, 0, 0, 0, loc), Until: time.Date(2026, 11, 2, 12, 0, 0, 0, loc)},
	}
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 11, 1, 12, 0, 0, 0, loc), "b"},
		{time.Date(2026, 11, 2, 6, 0, 0, 0, loc), "c"},
		{time.Date(2026, 11, 2, 12, 0, 0, 0, loc), "b"}, // the later one has ended
		{time.Date(2026, 11, 3, 0, 0, 0, 0, loc), ""},
		{time.Date(2026, 11, 2, 6, 0, 0, 0, loc).Add(0), "c"},
	} {
		o := Active("x", os, c.at)
		if (o == nil) != (c.want == "") || (o != nil && o.Member != c.want) {
			t.Errorf("%v: %+v, want %q", c.at, o, c.want)
		}
	}
	if o := Active("y", os, time.Date(2026, 11, 1, 12, 0, 0, 0, loc)); o != nil {
		t.Errorf("stand-in of another rotation applied: %+v", o)
	}
}
