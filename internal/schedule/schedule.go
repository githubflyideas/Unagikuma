// Package schedule describes when maintenance windows are open: once, every
// day, on some weekdays or days of the month at a time of day, or by a cron
// expression, each for a duration.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxDuration bounds a window; it also bounds how far back a recurring
// window is searched.
const MaxDuration = 7 * 24 * time.Hour

// Spec is a window schedule. Exactly one of At, Days/Weekdays/MonthDays
// (with Start) or Cron is set.
type Spec struct {
	At        time.Time // one-off start
	Daily     bool
	Weekdays  [7]bool  // indexed by time.Weekday
	MonthDays [32]bool // 1..31
	Start     int      // minutes after midnight, for the day rules
	Cron      *Cron
	Duration  time.Duration
}

// Active returns the open window containing t (in loc), if any.
func (s Spec) Active(t time.Time, loc *time.Location) (start, end time.Time, ok bool) {
	t = t.In(loc)
	switch {
	case !s.At.IsZero():
		start = s.At.In(loc)
		end = start.Add(s.Duration)
		return start, end, !t.Before(start) && t.Before(end)
	case s.Cron != nil:
		m := t.Truncate(time.Minute)
		for back := time.Duration(0); back < s.Duration; back += time.Minute {
			c := m.Add(-back)
			if s.Cron.Matches(c) && t.Before(c.Add(s.Duration)) {
				return c, c.Add(s.Duration), true
			}
		}
		return time.Time{}, time.Time{}, false
	}
	days := int(s.Duration/(24*time.Hour)) + 1
	y, mo, d := t.Date()
	for back := 0; back <= days; back++ {
		day := time.Date(y, mo, d-back, 0, 0, 0, 0, loc)
		if !s.dayMatches(day) {
			continue
		}
		st := day.Add(time.Duration(s.Start) * time.Minute)
		if !t.Before(st) && t.Before(st.Add(s.Duration)) {
			return st, st.Add(s.Duration), true
		}
	}
	return time.Time{}, time.Time{}, false
}

// Next returns the first window starting at or after t (within a year).
func (s Spec) Next(t time.Time, loc *time.Location) (time.Time, bool) {
	t = t.In(loc)
	switch {
	case !s.At.IsZero():
		at := s.At.In(loc)
		return at, !at.Before(t)
	case s.Cron != nil:
		return s.Cron.Next(t)
	}
	y, mo, d := t.Date()
	for i := 0; i <= 366; i++ {
		day := time.Date(y, mo, d+i, 0, 0, 0, 0, loc)
		st := day.Add(time.Duration(s.Start) * time.Minute)
		if s.dayMatches(day) && !st.Before(t) {
			return st, true
		}
	}
	return time.Time{}, false
}

func (s Spec) dayMatches(day time.Time) bool {
	return s.Daily || s.Weekdays[day.Weekday()] || s.MonthDays[day.Day()]
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseEvery parses the day rule of a recurring window: "day", a list of
// weekdays ("mon,fri", "mon-fri") or "month:1,15".
func ParseEvery(s string, sp *Spec) error {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "day" || s == "daily":
		sp.Daily = true
		return nil
	case strings.HasPrefix(s, "month:"):
		for _, p := range strings.Split(strings.TrimPrefix(s, "month:"), ",") {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || n < 1 || n > 31 {
				return fmt.Errorf("day of month %q must be 1-31", p)
			}
			sp.MonthDays[n] = true
		}
		return nil
	}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if a, b, ok := strings.Cut(p, "-"); ok {
			from, ok1 := weekdays[a]
			to, ok2 := weekdays[b]
			if !ok1 || !ok2 {
				return fmt.Errorf("unknown weekday range %q", p)
			}
			for d := from; ; d = (d + 1) % 7 {
				sp.Weekdays[d] = true
				if d == to {
					break
				}
			}
			continue
		}
		wd, ok := weekdays[p]
		if !ok {
			return fmt.Errorf("unknown day %q (use day, mon..sun or month:N)", p)
		}
		sp.Weekdays[wd] = true
	}
	return nil
}

// ParseClock parses "HH:MM" into minutes after midnight.
func ParseClock(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("time of day %q must be HH:MM", s)
	}
	return hh*60 + mm, nil
}

// ParseAt parses a one-off start: RFC 3339, or "2006-01-02 15:04" /
// "2006-01-02T15:04" in loc.
func ParseAt(s string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("start %q must look like 2026-11-01 22:00", s)
}

// Cron is a five-field cron expression: minute hour day-of-month month
// day-of-week. Fields take *, numbers, ranges, lists and /steps; when both
// day fields are restricted a day matching either one matches.
type Cron struct {
	min, hour, dom, mon, dow uint64
	domAny, dowAny           bool
	src                      string
}

// ParseCron parses a cron expression.
func ParseCron(s string) (*Cron, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q needs 5 fields: minute hour day month weekday", s)
	}
	c := &Cron{src: s}
	var err error
	if c.min, err = cronField(f[0], 0, 59); err != nil {
		return nil, err
	}
	if c.hour, err = cronField(f[1], 0, 23); err != nil {
		return nil, err
	}
	if c.dom, err = cronField(f[2], 1, 31); err != nil {
		return nil, err
	}
	if c.mon, err = cronField(f[3], 1, 12); err != nil {
		return nil, err
	}
	if c.dow, err = cronField(f[4], 0, 7); err != nil {
		return nil, err
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow = c.dow&^(1<<7) | 1
	}
	c.domAny, c.dowAny = f[2] == "*", f[4] == "*"
	return c, nil
}

func cronField(s string, lo, hi int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		step := 1
		if a, b, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(b)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("cron step %q", part)
			}
			part, step = a, n
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			x, err1 := strconv.Atoi(a)
			y, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("cron range %q", part)
			}
			from, to = x, y
		default:
			x, err := strconv.Atoi(part)
			if err != nil {
				return 0, fmt.Errorf("cron value %q", part)
			}
			from, to = x, x
		}
		if from < lo || to > hi || from > to {
			return 0, fmt.Errorf("cron value %q out of range %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func (c *Cron) String() string { return c.src }

func (c *Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	}
	return dom || dow
}

// Matches reports whether the minute of t is selected.
func (c *Cron) Matches(t time.Time) bool {
	return c.min&(1<<uint(t.Minute())) != 0 && c.hour&(1<<uint(t.Hour())) != 0 &&
		c.mon&(1<<uint(t.Month())) != 0 && c.dayMatches(t)
}

// Next returns the first matching minute at or after t, within a year.
func (c *Cron) Next(t time.Time) (time.Time, bool) {
	loc := t.Location()
	start := t.Truncate(time.Minute)
	if start.Before(t) {
		start = start.Add(time.Minute)
	}
	y, mo, d := start.Date()
	for i := 0; i <= 366; i++ {
		day := time.Date(y, mo, d+i, 0, 0, 0, 0, loc)
		if c.mon&(1<<uint(day.Month())) == 0 || !c.dayMatches(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if c.hour&(1<<uint(h)) == 0 {
				continue
			}
			for m := 0; m < 60; m++ {
				if c.min&(1<<uint(m)) == 0 {
					continue
				}
				at := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
				if !at.Before(start) {
					return at, true
				}
			}
		}
	}
	return time.Time{}, false
}
