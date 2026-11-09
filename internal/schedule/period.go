package schedule

import (
	"fmt"
	"strings"
	"time"
)

// Period is a weekly time period, like a Nagios timeperiod: rules such as
// "mon-fri 09:00-18:00; sat 10:00-14:00". A range whose end is not after
// its start runs past midnight into the next day ("daily 22:00-06:00");
// "24:00" ends at midnight.
type Period struct {
	Rules []PeriodRule
	Src   string
}

// PeriodRule is one "days from-to" rule.
type PeriodRule struct {
	Days     [7]bool
	From, To int // minutes after midnight; To may be <= From
}

// ParsePeriod parses rules separated by ";".
func ParsePeriod(s string) (Period, error) {
	p := Period{Src: strings.TrimSpace(s)}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		days, span, ok := strings.Cut(part, " ")
		span = strings.TrimSpace(span)
		if !ok || span == "" {
			days, span = part, "00:00-24:00"
		}
		var sp Spec
		if err := ParseEvery(days, &sp); err != nil {
			return p, err
		}
		if strings.HasPrefix(strings.ToLower(days), "month:") {
			return p, fmt.Errorf("periods are weekly: use day or mon..sun (month:N does not apply)")
		}
		r := PeriodRule{}
		for d := range r.Days {
			r.Days[d] = sp.Daily || sp.Weekdays[d]
		}
		a, b, ok := strings.Cut(span, "-")
		if !ok {
			return p, fmt.Errorf("time range %q must be HH:MM-HH:MM", span)
		}
		var err error
		if r.From, err = ParseClock(strings.TrimSpace(a)); err != nil {
			return p, err
		}
		if strings.TrimSpace(b) == "24:00" {
			r.To = 24 * 60
		} else if r.To, err = ParseClock(strings.TrimSpace(b)); err != nil {
			return p, err
		}
		if r.From == r.To {
			return p, fmt.Errorf("time range %q is empty", span)
		}
		p.Rules = append(p.Rules, r)
	}
	if len(p.Rules) == 0 {
		return p, fmt.Errorf("empty period")
	}
	return p, nil
}

// Contains reports whether t (in loc) falls inside the period.
func (p Period) Contains(t time.Time, loc *time.Location) bool {
	t = t.In(loc)
	min := t.Hour()*60 + t.Minute()
	today, yesterday := t.Weekday(), (t.Weekday()+6)%7
	for _, r := range p.Rules {
		if r.To > r.From {
			if r.Days[today] && min >= r.From && min < r.To {
				return true
			}
			continue
		}
		// Past midnight: the evening part today, the morning part from
		// the rule of the day before.
		if (r.Days[today] && min >= r.From) || (r.Days[yesterday] && min < r.To) {
			return true
		}
	}
	return false
}
