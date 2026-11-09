package app

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/oncall"
)

func TestCalendarSegments(t *testing.T) {
	loc := time.UTC
	r, err := oncall.Parse(map[string]string{"rotate": "daily", "handoff": "09:00", "start": "2026-11-01"}, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, loc) }
	// Midnight to midnight: the first and last shifts are whole.
	segs := calendar(r, "x", nil, at(2, 0), at(4, 0), loc)
	want := []struct {
		who      string
		from, to time.Time
	}{{"a", at(1, 9), at(2, 9)}, {"b", at(2, 9), at(3, 9)}, {"c", at(3, 9), at(4, 9)}}
	if len(segs) != len(want) {
		t.Fatalf("segments: %+v", segs)
	}
	for i, w := range want {
		if s := segs[i]; s.Member != w.who || s.Scheduled != w.who || !s.From.Equal(w.from) || !s.Until.Equal(w.to) {
			t.Errorf("%d: %+v, want %+v", i, s, w)
		}
	}
	// A stand-in that ended before from still shows in the first shift;
	// one inside a shift splits it; a stand-in running into the stand-in's own
	// shift stays two segments (whose shift it is differs).
	os := []oncall.Override{
		{ID: 1, Rotation: "x", Member: "c", From: at(1, 10), Until: at(1, 12)},
		{ID: 2, Rotation: "x", Member: "c", From: at(2, 12), Until: at(2, 18), Note: "n"},
		{ID: 3, Rotation: "x", Member: "c", From: at(3, 7), Until: at(3, 9)},
		{ID: 4, Rotation: "y", Member: "a", From: at(2, 0), Until: at(4, 0)},
	}
	segs = calendar(r, "x", os, at(2, 0), at(3, 12), loc)
	got := ""
	for _, s := range segs {
		got += s.Member + "/" + s.Scheduled + "@" + s.From.Format("02T15") + " "
		if s.StandIn == 2 && s.Note != "n" {
			t.Errorf("note: %+v", s)
		}
	}
	if got != "a/a@01T09 c/a@01T10 a/a@01T12 b/b@02T09 c/b@02T12 b/b@02T18 c/b@03T07 c/c@03T09 " {
		t.Fatalf("segments: %s", got)
	}
	if last := segs[len(segs)-1]; !last.Until.Equal(at(4, 9)) {
		t.Fatalf("last segment cut: %+v", last)
	}
}
