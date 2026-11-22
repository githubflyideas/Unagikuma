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
		// The snapshot still carries gateway/uplink as structured data, but
		// the rendered line must end after the node list: no gateway/uplink
		// suffix, whatever the values are.
		if !strings.Contains(body, "a ✗, b ✓, c ✓") || strings.Contains(body, "2/3") ||
			strings.Contains(body, "网关") || strings.Contains(body, "上联") || strings.Contains(body, "wan_down") {
			t.Fatal(kind, body)
		}
	}
	// Same rule in English, pinned to the exact line.
	m := &Message{Kind: "PROBLEM", Monitor: "web", Lang: "en", Observations: &observation.Snapshot{Rows: []observation.Row{{NodeID: "a", Status: "down"}, {NodeID: "b", Status: "up"}}, Gateway: "ok", Uplink: "ok"}}
	_, body := Render(m)
	if !strings.Contains(body, "Observations: a ✗, b ✓") || strings.Contains(body, "gateway:") || strings.Contains(body, "uplink:") {
		t.Fatal(body)
	}
	m = &Message{Kind: "PROBLEM", Lang: "zh"}
	_, body = Render(m)
	if strings.Contains(body, "各站观测") {
		t.Fatal("single node has cluster context", body)
	}
}
