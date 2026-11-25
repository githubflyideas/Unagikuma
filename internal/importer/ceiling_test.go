package importer

import (
	"fmt"
	"strings"
	"testing"
)

// The import ceiling: an import may take the counted total to the
// limit and no further. Monitors past it are left out one by one with
// the ceiling as the reason, their records go with them, and the
// result states the final total. A ceiling left-out does not block a
// Gatus apply the way an unsupported item does.
func TestImportCeiling(t *testing.T) {
	var b strings.Builder
	b.WriteString("endpoints:\n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "  - name: ep%d\n    url: https://example.invalid/%d\n    conditions: ['[STATUS] == 200']\n", i, i)
	}
	r, err := Gatus([]byte(b.String()), nil, Options{TotalNow: 298, TotalLimit: 300})
	if err != nil {
		t.Fatal(err)
	}
	if r.Monitors != 2 || len(r.Records) != 2 || r.TotalAfter != 300 {
		t.Fatalf("kept: monitors=%d records=%d total=%d", r.Monitors, len(r.Records), r.TotalAfter)
	}
	left := 0
	for _, it := range r.Items {
		if it.Status == StatusLeftOut {
			left++
			if !strings.Contains(it.Why, "300") {
				t.Fatalf("left-out reason: %q", it.Why)
			}
		}
	}
	if left != 3 {
		t.Fatalf("left out: %d", left)
	}
	if err := r.ActivationError(); err != nil {
		t.Fatalf("ceiling left-outs block the apply: %v", err)
	}
	rep := r.Report(ReportOptions{})
	if !strings.Contains(rep, "Monitors after this import: 300") {
		t.Fatalf("report does not state the total:\n%s", rep)
	}
	// No ceiling in force: everything comes over, no total is stated.
	r2, err := Gatus([]byte(b.String()), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Monitors != 5 || r2.TotalAfter != 0 {
		t.Fatalf("no ceiling: monitors=%d total=%d", r2.Monitors, r2.TotalAfter)
	}
	// Already at the ceiling: nothing more comes over.
	r3, err := Gatus([]byte(b.String()), nil, Options{TotalNow: 300, TotalLimit: 300})
	if err != nil {
		t.Fatal(err)
	}
	if r3.Monitors != 0 || len(r3.Records) != 0 || r3.TotalAfter != 300 {
		t.Fatalf("at the ceiling: monitors=%d records=%d total=%d", r3.Monitors, len(r3.Records), r3.TotalAfter)
	}
}
