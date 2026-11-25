package observation

import "testing"

// TestSummaryGlyphs pins the decided node-status glyphs (✓ fresh up,
// ✗ fresh down, ⊘ stale) and the removal of the trailing up-fraction.
func TestSummaryGlyphs(t *testing.T) {
	cases := []struct {
		name string
		rows []Row
		want string
	}{
		{"mixed with stale", []Row{
			{NodeID: "nodea", Status: "down"},
			{NodeID: "nodeb", Status: "up"},
			{NodeID: "nodec", Status: "up", Stale: true},
		}, "nodea ✗, nodeb ✓, nodec ⊘"},
		{"all down with stale", []Row{
			{NodeID: "nodea", Status: "down"},
			{NodeID: "nodeb", Status: "down"},
			{NodeID: "nodec", Status: "down", Stale: true},
		}, "nodea ✗, nodeb ✗, nodec ⊘"},
		{"single up", []Row{
			{NodeID: "nodea", Status: "up"},
		}, "nodea ✓"},
		{"single stale", []Row{
			{NodeID: "nodea", Status: "up", Stale: true},
		}, "nodea ⊘"},
		{"no rows", nil, ""},
	}
	for _, tc := range cases {
		if got := (Snapshot{Rows: tc.rows}).Summary(); got != tc.want {
			t.Errorf("%s: Summary() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
