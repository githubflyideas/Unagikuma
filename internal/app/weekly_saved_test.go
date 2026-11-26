package app

import (
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
)

// The weekly report's count of alerts held back leaves out the blips that
// recovered before the failure count: they would never have alerted.
func TestWeeklySavedLeavesOutBlips(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	a.Store().Counter(engine.CounterSoftFiltered, 5)
	a.Store().Counter(engine.CounterCapSaved, 2)
	a.Store().Counter(engine.CounterOutageSaved, 1)
	a.Store().Flush()
	r := a.weeklyReport(time.Now())
	if !strings.Contains(r.Body, "Suppressed alerts: 3") {
		t.Fatalf("held back count: %q", r.Body)
	}
}
