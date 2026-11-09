package notify

import (
	"github.com/githubflyideas/unagikuma/internal/observation"
	"strings"
	"testing"
)

func TestObservationSummaryOnProblemAndRecovery(t *testing.T) {
	for _, kind := range []string{"PROBLEM", "RECOVERY"} {
		m := &Message{Kind: kind, Monitor: "web", Lang: "zh", Observations: &observation.Snapshot{Rows: []observation.Row{{NodeID: "a", Status: "down"}, {NodeID: "b", Status: "up"}, {NodeID: "c", Status: "up"}}, Gateway: "down", Uplink: "wan_down"}}
		_, body := Render(m)
		if !strings.Contains(body, "a ×, b ○, c ○, 2/3") || !strings.Contains(body, "网关：down") {
			t.Fatal(kind, body)
		}
	}
	m := &Message{Kind: "PROBLEM", Lang: "zh"}
	_, body := Render(m)
	if strings.Contains(body, "各站观测") {
		t.Fatal("single node has cluster context", body)
	}
}
