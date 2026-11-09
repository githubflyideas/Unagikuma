// Package oncall works out who is on call. A rotation is a contact group
// channel with rotate= set: instead of notifying every member at once, it
// notifies the member whose shift it is, and the others only when that
// fails. Stand-ins cover a shift for a while without changing the order.
package oncall

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options of a group channel that make it a rotation.
const (
	OptRotate  = "rotate"  // daily | weekly | Nd | Nh
	OptHandoff = "handoff" // "mon 09:00" (whole weeks) or "09:00"
	OptStart   = "start"   // YYYY-MM-DD: the first member's first shift
	OptTZ      = "tz"      // IANA time zone of handoff and start (default: the server's)
)

// IsOption reports whether key is a rotation option.
func IsOption(key string) bool {
	return key == OptRotate || key == OptHandoff || key == OptStart || key == OptTZ
}

// defaultStart anchors rotations without start=: a Monday, so weekly
// rotations handing off on Mondays begin with the first member there.
var defaultStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// Rotation is the shift plan of a group.
type Rotation struct {
	Members []string
	// Days is the shift length in calendar days (0 for hour shifts), so a
	// handoff stays at the same local time across daylight saving changes.
	Days  int
	Hours int
	// Weekday is the handoff day of shifts of whole weeks (-1: any).
	Weekday time.Weekday
	Clock   int // handoff, minutes after midnight
	Start   time.Time
	Src     string // the rotate= value, for display
	Handoff string // the handoff= value as written, or the default
	// Loc is the time zone of tz=; nil: the server's time zone, which
	// the callers pass in.
	Loc *time.Location
	TZ  string
}

// zone is the rotation's own time zone, else loc.
func (r *Rotation) zone(loc *time.Location) *time.Location {
	if r.Loc != nil {
		return r.Loc
	}
	if loc == nil {
		return time.Local
	}
	return loc
}

var dayNames = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// Parse reads the rotation options of a group channel; nil without
// rotate=. Handoff and start without rotate= are an error.
func Parse(opts map[string]string, members []string) (*Rotation, error) {
	rot, ok := opts[OptRotate]
	if !ok {
		for _, k := range []string{OptHandoff, OptStart, OptTZ} {
			if _, set := opts[k]; set {
				return nil, fmt.Errorf("%s needs rotate=", k)
			}
		}
		return nil, nil
	}
	r := &Rotation{Members: members, Weekday: -1, Start: defaultStart, Src: rot}
	switch v := strings.ToLower(strings.TrimSpace(rot)); {
	case v == "daily":
		r.Days = 1
	case v == "weekly":
		r.Days = 7
	case strings.HasSuffix(v, "d") || strings.HasSuffix(v, "h"):
		n, err := strconv.Atoi(v[:len(v)-1])
		if err != nil || n < 1 || (v[len(v)-1] == 'd' && n > 90) || (v[len(v)-1] == 'h' && n > 720) {
			return nil, fmt.Errorf("rotate must be daily, weekly, 1d to 90d or 1h to 720h, got %q", rot)
		}
		if v[len(v)-1] == 'd' {
			r.Days = n
		} else {
			r.Hours = n
		}
	default:
		return nil, fmt.Errorf("rotate must be daily, weekly, Nd or Nh, got %q", rot)
	}
	r.Handoff = opts[OptHandoff]
	if r.Handoff == "" {
		r.Handoff = "09:00"
		if r.Days > 0 && r.Days%7 == 0 {
			r.Handoff = "mon 09:00"
		}
	}
	f := strings.Fields(strings.ToLower(r.Handoff))
	if len(f) == 2 {
		d, ok := dayNames[f[0][:min(3, len(f[0]))]]
		if !ok {
			return nil, fmt.Errorf("handoff: unknown day %q", f[0])
		}
		if r.Days == 0 || r.Days%7 != 0 {
			return nil, fmt.Errorf("handoff: a day applies only to shifts of whole weeks")
		}
		r.Weekday = d
		f = f[1:]
	}
	if len(f) != 1 {
		return nil, fmt.Errorf("handoff must look like \"mon 09:00\" or \"09:00\", got %q", r.Handoff)
	}
	c, err := time.Parse("15:04", f[0])
	if err != nil {
		return nil, fmt.Errorf("handoff: time must be HH:MM, got %q", f[0])
	}
	r.Clock = c.Hour()*60 + c.Minute()
	if s := opts[OptStart]; s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, fmt.Errorf("start must be a date like 2026-11-02, got %q", s)
		}
		r.Start = t
	}
	if tz := strings.TrimSpace(opts[OptTZ]); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil || strings.EqualFold(tz, "local") {
			return nil, fmt.Errorf("tz must be a time zone like Asia/Shanghai or UTC, got %q", tz)
		}
		r.Loc, r.TZ = l, tz
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("a rotation needs members")
	}
	return r, nil
}

// anchor is the start of shift 0 in loc: the shift that contains the
// start date's handoff, so the first member is on call from that day on
// even when it is not a handoff day.
func (r *Rotation) anchor(loc *time.Location) time.Time {
	y, m, d := r.Start.Date()
	if r.Weekday >= 0 {
		wd := time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Weekday()
		d -= (int(wd) - int(r.Weekday) + 7) % 7
	}
	return wallTime(y, m, d, r.Clock, r.zone(loc))
}

// wallTime is the instant of minute (after midnight; may overflow into
// the next days) on a local date. A handoff inside a daylight saving gap
// moves to the end of the gap; one that happens twice when clocks go back
// is the first of the two.
func wallTime(y int, m time.Month, d, minute int, loc *time.Location) time.Time {
	d += floorDiv(minute, 24*60)
	minute -= floorDiv(minute, 24*60) * 24 * 60
	h, mi := minute/60, minute%60
	t := time.Date(y, m, d, h, mi, 0, 0, loc)
	if t.Hour() != h || t.Minute() != mi {
		// In a gap: the first instant after it is the zone change.
		start, end := t.ZoneBounds()
		if t.Hour()*60+t.Minute() < h*60+mi && !end.IsZero() {
			return end
		}
		return start
	}
	// Happens twice: take the earlier one.
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute, 2 * time.Hour} {
		if e := t.Add(-back); e.Hour() == h && e.Minute() == mi && e.Day() == t.Day() {
			return e
		}
	}
	return t
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// shiftStart is when shift k begins: on the local clock, so handoffs stay
// at the same local time across daylight saving changes (a day shift that
// spans one lasts 23 or 25 hours).
func (r *Rotation) shiftStart(a time.Time, k int) time.Time {
	y, m, d := r.Start.Date()
	if r.Weekday >= 0 {
		wd := time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Weekday()
		d -= (int(wd) - int(r.Weekday) + 7) % 7
	}
	if r.Days > 0 {
		return wallTime(y, m, d+k*r.Days, r.Clock, a.Location())
	}
	return wallTime(y, m, d, r.Clock+k*r.Hours*60, a.Location())
}

// shiftAt returns the index of the shift running at t (negative before
// the start: the order simply runs backwards).
func (r *Rotation) shiftAt(a, t time.Time) int {
	length := time.Duration(r.Hours) * time.Hour
	if r.Days > 0 {
		length = time.Duration(r.Days) * 24 * time.Hour
	}
	k := int(t.Sub(a) / length)
	for r.shiftStart(a, k).After(t) {
		k--
	}
	for !r.shiftStart(a, k+1).After(t) {
		k++
	}
	return k
}

func (r *Rotation) member(k int) string {
	n := len(r.Members)
	return r.Members[((k%n)+n)%n]
}

// Shift is one turn of a member.
type Shift struct {
	Member string    `json:"member"`
	From   time.Time `json:"from"`
	Until  time.Time `json:"until"`
}

// At returns the scheduled shift at t, stand-ins aside.
func (r *Rotation) At(t time.Time, loc *time.Location) Shift {
	a := r.anchor(loc)
	k := r.shiftAt(a, t)
	return Shift{Member: r.member(k), From: r.shiftStart(a, k), Until: r.shiftStart(a, k+1)}
}

// Next returns the scheduled shifts from the one running at t on, n of them.
func (r *Rotation) Next(t time.Time, n int, loc *time.Location) []Shift {
	a := r.anchor(loc)
	k := r.shiftAt(a, t)
	out := make([]Shift, 0, n)
	for i := range n {
		out = append(out, Shift{Member: r.member(k + i), From: r.shiftStart(a, k+i), Until: r.shiftStart(a, k+i+1)})
	}
	return out
}

// Override has Member stand in for whoever is scheduled on Rotation
// between From and Until.
type Override struct {
	ID        int64     `json:"id"`
	Rotation  string    `json:"rotation"`
	Member    string    `json:"member"`
	From      time.Time `json:"from"`
	Until     time.Time `json:"until"`
	By        string    `json:"by"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Covers reports whether the stand-in applies to rotation at t.
func (o Override) Covers(rotation string, t time.Time) bool {
	return o.Rotation == rotation && !t.Before(o.From) && t.Before(o.Until)
}

// Active returns the stand-in of rotation at t: the newest one covering
// it, since a later arrangement replaces an earlier one.
func Active(rotation string, overrides []Override, t time.Time) *Override {
	var best *Override
	for i := range overrides {
		o := &overrides[i]
		if o.Covers(rotation, t) && (best == nil || o.ID > best.ID) {
			best = o
		}
	}
	return best
}

// Order returns the members in the order they are tried at t: whoever is
// on call (a stand-in first, then the scheduled member), then the others
// in rotation order after the scheduled one.
func (r *Rotation) Order(rotation string, overrides []Override, t time.Time, loc *time.Location) []string {
	a := r.anchor(loc)
	k := r.shiftAt(a, t)
	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	if o := Active(rotation, overrides, t); o != nil {
		add(o.Member)
	}
	for i := range r.Members {
		add(r.member(k + i))
	}
	return out
}

// SortOverrides orders stand-ins by start, then by creation.
func SortOverrides(os []Override) {
	sort.Slice(os, func(i, j int) bool {
		if !os[i].From.Equal(os[j].From) {
			return os[i].From.Before(os[j].From)
		}
		return os[i].ID < os[j].ID
	})
}
