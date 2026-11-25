package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/observation"
)

// An alert's site line says, for every other site, whether its word came
// after the failure began here or from before it, and how long before.
// The sending site and sites with no recent word get no such note.
func TestObservationText(t *testing.T) {
	since := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	s := &observation.Snapshot{Since: since, Rows: []observation.Row{
		{NodeID: "a", Status: "down", ReceivedAt: since.Add(30 * time.Second)},
		{NodeID: "b", Status: "up", ReceivedAt: since.Add(8 * time.Second), AgeMS: 2000},
		{NodeID: "c", Status: "up", ReceivedAt: since.Add(-10 * time.Second), AgeMS: 30000},
		{NodeID: "d", Status: "up", Stale: true},
	}}
	got := ObservationText("en", s, "a")
	for _, want := range []string{"a ✗", "b ✓ (checked after the failure)", "c ✓ (40s before the failure)", "d ⊘"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "a ✗ (") || strings.Contains(got, "d ⊘ (") {
		t.Errorf("note on the sender or a stale site: %q", got)
	}
	// Not about a failure (no Since): marks only.
	plain := ObservationText("en", &observation.Snapshot{Rows: s.Rows}, "a")
	if strings.Contains(plain, "(") {
		t.Errorf("notes without a failure: %q", plain)
	}
	if zh := ObservationText("zh", s, "a"); !strings.Contains(zh, "失败后复查") || !strings.Contains(zh, "故障前") {
		t.Errorf("zh: %q", zh)
	}
}
