package app

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/oncall"
)

// CalendarSegment is a stretch of time with one person on call: the
// scheduled member, or a stand-in covering them.
type CalendarSegment struct {
	Member    string    `json:"member"`             // who is notified first
	Scheduled string    `json:"scheduled"`          // whose shift it is
	StandIn   int64     `json:"stand_in,omitempty"` // the stand-in id, when one covers it
	Note      string    `json:"note,omitempty"`     // the stand-in's note
	From      time.Time `json:"from"`
	Until     time.Time `json:"until"`
}

// CalendarRotation is one rotation's calendar.
type CalendarRotation struct {
	Name     string            `json:"name"`
	TZ       string            `json:"tz"`
	Members  []OnCallMember    `json:"members"`
	Segments []CalendarSegment `json:"segments"`
}

// maxCalendar bounds a calendar request.
const maxCalendar = 92 * 24 * time.Hour

// OnCallCalendar lays out who is on call in [from, to) for every rotation,
// stand-ins applied, cut where a shift or a stand-in begins or ends. The
// first and the last segment are whole: they may begin before from and
// end after to.
func (a *App) OnCallCalendar(from, to time.Time) ([]CalendarRotation, error) {
	if !to.After(from) || to.Sub(from) > maxCalendar {
		return nil, fmt.Errorf("%w: the calendar covers up to 92 days", ErrOnCall)
	}
	cfg := a.Config().Config
	// Stand-ins ended before from still shape the first, whole shift.
	os, err := a.store.Overrides(from.Add(-91 * 24 * time.Hour))
	if err != nil {
		return nil, err
	}
	dir := a.Directory()
	out := []CalendarRotation{}
	for name, r := range rotations(cfg) {
		cr := CalendarRotation{Name: name, TZ: rotationZone(r, a.opts.Location), Segments: []CalendarSegment{}}
		for _, m := range r.Members {
			cr.Members = append(cr.Members, memberOf(cfg, dir, m))
		}
		cr.Segments = calendar(r, name, os, from, to, a.opts.Location)
		out = append(out, cr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// rotationZone names the time zone a rotation hands off in.
func rotationZone(r *oncall.Rotation, loc *time.Location) string {
	if r.TZ != "" {
		return r.TZ
	}
	return zoneName(loc)
}

// zoneName names a time zone for people: "Local" becomes the zone the
// system is set to (TZ, else /etc/localtime), or its offset.
func zoneName(loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	if loc != time.Local && loc.String() != "Local" {
		return loc.String()
	}
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" && !strings.HasPrefix(tz, "/") {
		return tz
	}
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(p, "zoneinfo/"); ok && name != "" {
			return name
		}
	}
	_, off := time.Now().In(loc).Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, off/3600, off%3600/60)
}

func calendar(r *oncall.Rotation, name string, os []oncall.Override, from, to time.Time, loc *time.Location) []CalendarSegment {
	// Every shift boundary from the start of the shift running at from to
	// the end of the one running at to, and the edges of the stand-ins in
	// between: the first and last shifts show their whole length.
	first := r.At(from, loc)
	cuts := []time.Time{first.From}
	for _, s := range r.Next(from, 4000, loc) {
		cuts = append(cuts, s.Until)
		if !s.Until.Before(to) {
			break
		}
	}
	lo, hi := first.From, cuts[len(cuts)-1]
	for _, o := range os {
		if o.Rotation != name {
			continue
		}
		for _, c := range []time.Time{o.From, o.Until} {
			if c.After(lo) && c.Before(hi) {
				cuts = append(cuts, c)
			}
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Before(cuts[j]) })
	cuts = slices.CompactFunc(cuts, func(x, y time.Time) bool { return x.Equal(y) })
	var segs []CalendarSegment
	for i := 0; i+1 < len(cuts); i++ {
		at := cuts[i]
		sg := CalendarSegment{Scheduled: r.At(at, loc).Member, From: at, Until: cuts[i+1]}
		sg.Member = sg.Scheduled
		if o := oncall.Active(name, os, at); o != nil {
			sg.Member, sg.StandIn, sg.Note = o.Member, o.ID, o.Note
		}
		if n := len(segs); n > 0 && segs[n-1].Member == sg.Member && segs[n-1].Scheduled == sg.Scheduled && segs[n-1].StandIn == sg.StandIn {
			segs[n-1].Until = sg.Until
			continue
		}
		segs = append(segs, sg)
	}
	if segs == nil {
		segs = []CalendarSegment{}
	}
	return segs
}

// SwapShifts swaps two shifts of a rotation: the member of the shift
// running at b covers the shift running at a, and the other way round, as
// a pair of stand-ins. The order of the rotation does not change.
func (a *App) SwapShifts(rotation string, atA, atB time.Time, by, note string) ([]oncall.Override, error) {
	r := rotations(a.Config().Config)[rotation]
	if r == nil {
		return nil, fmt.Errorf("%w: %s is not an on-call rotation", ErrOnCall, rotation)
	}
	loc, now := a.opts.Location, a.now()
	sa, sb := r.At(atA, loc), r.At(atB, loc)
	switch {
	case sa.Member == sb.Member:
		return nil, fmt.Errorf("%w: both shifts are %s's", ErrOnCall, sa.Member)
	case !sa.Until.After(now) || !sb.Until.After(now):
		return nil, fmt.Errorf("%w: a shift that is over cannot be swapped", ErrOnCall)
	}
	var out []oncall.Override
	for _, o := range []oncall.Override{
		{Rotation: rotation, Member: sb.Member, From: sa.From, Until: sa.Until, By: by, Note: note, CreatedAt: now},
		{Rotation: rotation, Member: sa.Member, From: sb.From, Until: sb.Until, By: by, Note: note, CreatedAt: now},
	} {
		saved, err := a.store.AddOverride(o)
		if err != nil {
			return out, err
		}
		out = append(out, saved)
	}
	a.reapply()
	a.emit("oncall", nil)
	return out, nil
}
