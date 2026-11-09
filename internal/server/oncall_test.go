package server

import (
	"net/url"
	"strconv"
	"testing"
	"time"
)

const oncallBind = testBind + `li     IN CHANNEL log
wang   IN CHANNEL log
oncall IN CHANNEL group members=li,wang rotate=weekly handoff="mon 09:00"
pager  IN POLICY chain=oncall
`

func TestOnCallAPI(t *testing.T) {
	e := newEnv(t, oncallBind, nil)
	e.setup()
	type rotation struct {
		Name, Now, Rotate, Handoff string
		Members                    []struct{ Channel string }
		Shifts                     []struct {
			Member   string
			StandIns []struct{ Member string } `json:"stand_ins"`
		}
		StandIn  *struct{ ID int64 } `json:"stand_in"`
		Policies []string
	}
	var view struct {
		Rotations []rotation
		Stats     struct {
			Members []struct{ Rotation, Member string }
		}
	}
	if c := e.do("GET", "/api/v1/oncall", nil, &view); c != 200 || len(view.Rotations) != 1 {
		t.Fatalf("view: %d %+v", c, view)
	}
	ro := view.Rotations[0]
	if ro.Name != "oncall" || ro.Rotate != "weekly" || ro.Handoff != "mon 09:00" || len(ro.Members) != 2 || len(ro.Shifts) != 8 ||
		ro.StandIn != nil || len(ro.Policies) != 1 || ro.Policies[0] != "pager" || len(view.Stats.Members) != 2 {
		t.Fatalf("rotation: %+v stats %+v", ro, view.Stats)
	}
	other := "li"
	if ro.Now == "li" {
		other = "wang"
	}
	for _, bad := range []map[string]any{
		{"rotation": "nope", "member": "li", "until": time.Now().Add(time.Hour)},
		{"rotation": "oncall", "member": "boss", "until": time.Now().Add(time.Hour)},
		{"rotation": "oncall", "member": "li", "until": time.Now().Add(-time.Hour)},
	} {
		if c := e.do("POST", "/api/v1/oncall/overrides", bad, nil); c != 400 {
			t.Errorf("%v: %d", bad, c)
		}
	}
	var o struct{ ID int64 }
	if c := e.do("POST", "/api/v1/oncall/overrides", map[string]any{"rotation": "oncall", "member": other, "until": time.Now().Add(2 * time.Hour), "note": "swap"}, &o); c != 200 || o.ID == 0 {
		t.Fatalf("stand-in: %d %+v", c, o)
	}
	e.do("GET", "/api/v1/oncall", nil, &view)
	ro = view.Rotations[0]
	if ro.Now != other || ro.StandIn == nil || ro.StandIn.ID != o.ID || len(ro.Shifts[0].StandIns) != 1 {
		t.Fatalf("after the stand-in: %+v", ro)
	}
	// The engine notifies the stand-in first.
	if got := e.app.Engine().OnCallOrder("oncall", time.Now()); len(got) != 2 || got[0] != other {
		t.Fatalf("engine order: %v", got)
	}
	if c := e.do("DELETE", "/api/v1/oncall/overrides/"+strconv.FormatInt(o.ID, 10), nil, nil); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	if c := e.do("DELETE", "/api/v1/oncall/overrides/"+strconv.FormatInt(o.ID, 10), nil, nil); c != 404 {
		t.Fatalf("delete twice: %d", c)
	}
	e.do("GET", "/api/v1/oncall", nil, &view)
	if view.Rotations[0].Now == other {
		t.Fatal("the stand-in is still on call")
	}
}

const calendarBind = testBind + `li     IN CHANNEL log
wang   IN CHANNEL log
zhao   IN CHANNEL log
oncall IN CHANNEL group members=li,wang,zhao rotate=daily handoff=09:00 start=2026-01-01 tz=Asia/Shanghai
pager  IN POLICY chain=oncall
`

type calSeg struct {
	Member, Scheduled string
	StandIn           int64 `json:"stand_in"`
	From, Until       time.Time
}

func TestOnCallCalendarAndSwap(t *testing.T) {
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("no time zone data")
	}
	e := newEnv(t, calendarBind, nil)
	e.setup()
	var cal struct {
		Rotations []struct {
			Name, TZ string
			Segments []calSeg
		}
	}
	get := func(from, to time.Time) int {
		cal.Rotations = nil
		return e.do("GET", "/api/v1/oncall/calendar?from="+url.QueryEscape(from.Format(time.RFC3339))+"&to="+url.QueryEscape(to.Format(time.RFC3339)), nil, &cal)
	}
	// Tomorrow 09:00 in Shanghai, wherever the test runs.
	y, m, d := time.Now().In(sh).Date()
	day := time.Date(y, m, d+1, 9, 0, 0, 0, sh)
	if c := get(day, day.Add(3*24*time.Hour)); c != 200 || len(cal.Rotations) != 1 || cal.Rotations[0].TZ != "Asia/Shanghai" {
		t.Fatalf("calendar: %d %+v", c, cal)
	}
	segs := cal.Rotations[0].Segments
	if len(segs) != 3 {
		t.Fatalf("three days, three shifts: %+v", segs)
	}
	for i, s := range segs {
		if !s.From.Equal(day.Add(time.Duration(i)*24*time.Hour)) || !s.Until.Equal(s.From.Add(24*time.Hour)) || s.Member != s.Scheduled || s.StandIn != 0 {
			t.Fatalf("shift %d: %+v", i, s)
		}
	}
	if segs[0].Member == segs[1].Member || segs[1].Member == segs[2].Member {
		t.Fatalf("no rotation: %+v", segs)
	}
	for _, bad := range []string{"from=yesterday", "from=" + url.QueryEscape(day.Format(time.RFC3339)) + "&to=" + url.QueryEscape(day.Add(-time.Hour).Format(time.RFC3339)),
		"from=" + url.QueryEscape(day.Format(time.RFC3339)) + "&to=" + url.QueryEscape(day.Add(100*24*time.Hour).Format(time.RFC3339))} {
		if c := e.do("GET", "/api/v1/oncall/calendar?"+bad, nil, nil); c != 400 {
			t.Errorf("%s: %d", bad, c)
		}
	}
	// A stand-in in the middle of the first shift cuts it in three.
	var o struct{ ID int64 }
	if c := e.do("POST", "/api/v1/oncall/overrides", map[string]any{"rotation": "oncall", "member": segs[2].Member,
		"from": day.Add(6 * time.Hour), "until": day.Add(8 * time.Hour)}, &o); c != 200 {
		t.Fatalf("stand-in: %d", c)
	}
	get(day, day.Add(24*time.Hour))
	if s := cal.Rotations[0].Segments; len(s) != 3 || s[1].Member != segs[2].Member || s[1].Scheduled != segs[0].Member || s[1].StandIn != o.ID ||
		!s[1].From.Equal(day.Add(6*time.Hour)) || !s[1].Until.Equal(day.Add(8*time.Hour)) || s[0].Member != segs[0].Member || s[2].Member != segs[0].Member {
		t.Fatalf("stand-in in the calendar: %+v", s)
	}
	// Swap the first and the third day.
	var sw struct {
		StandIns []struct{ ID int64 } `json:"stand_ins"`
	}
	if c := e.do("POST", "/api/v1/oncall/swap", map[string]any{"rotation": "oncall", "a": day.Add(time.Hour), "b": day.Add(49 * time.Hour), "note": "holiday"}, &sw); c != 200 || len(sw.StandIns) != 2 {
		t.Fatalf("swap: %d %+v", c, sw)
	}
	get(day.Add(8*time.Hour), day.Add(3*24*time.Hour))
	s := cal.Rotations[0].Segments
	if len(s) != 3 || s[0].Member != segs[2].Member || s[0].Scheduled != segs[0].Member || s[1].Member != segs[1].Member || s[1].StandIn != 0 ||
		s[2].Member != segs[0].Member || s[2].Scheduled != segs[2].Member {
		t.Fatalf("after the swap: %+v", s)
	}
	// The engine follows the calendar.
	if got := e.app.Engine().OnCallOrder("oncall", day.Add(50*time.Hour)); len(got) == 0 || got[0] != segs[0].Member {
		t.Fatalf("engine on the swapped day: %v", got)
	}
	for _, bad := range []map[string]any{
		{"rotation": "nope", "a": day, "b": day.Add(24 * time.Hour)},
		{"rotation": "oncall", "a": day.Add(time.Hour), "b": day.Add(2 * time.Hour)},        // the same shift
		{"rotation": "oncall", "a": day.Add(-72 * time.Hour), "b": day.Add(24 * time.Hour)}, // over
		{"rotation": "oncall", "a": day.Add(time.Hour)},                                     // b missing
	} {
		if c := e.do("POST", "/api/v1/oncall/swap", bad, nil); c != 400 {
			t.Errorf("%v: %d", bad, c)
		}
	}
}
