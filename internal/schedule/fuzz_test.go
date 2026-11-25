package schedule

import (
	"testing"
	"time"
)

func FuzzCron(f *testing.F) {
	f.Add("*/15 2-5 1,15 * 1-5")
	f.Add("0 0 * * 7")
	f.Fuzz(func(t *testing.T, s string) {
		c, err := ParseCron(s)
		if err != nil {
			return
		}
		now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
		if next, ok := c.Next(now); ok && (next.Before(now) || !c.Matches(next)) {
			t.Fatalf("%q: next %v does not match", s, next)
		}
		Spec{Cron: c, Duration: time.Hour}.Active(now, time.UTC)
	})
}

func FuzzEvery(f *testing.F) {
	f.Add("mon-fri", "02:30")
	f.Add("month:1,31", "23:59")
	f.Fuzz(func(t *testing.T, every, clock string) {
		var sp Spec
		if ParseEvery(every, &sp) != nil {
			return
		}
		m, err := ParseClock(clock)
		if err != nil {
			return
		}
		sp.Start, sp.Duration = m, 2*time.Hour
		now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
		sp.Active(now, time.UTC)
		if next, ok := sp.Next(now, time.UTC); ok && next.Before(now) {
			t.Fatal("next in the past")
		}
	})
}
