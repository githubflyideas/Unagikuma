package engine

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/schedule"
)

func TestMaintenanceHoldsAndReleases(t *testing.T) {
	start := time.Now()
	d := newDepEnv(t, MaintSpec{Name: "work", Title: "switch upgrade", Monitors: []string{"gw"},
		Schedule: schedule.Spec{At: start, Duration: 1500 * time.Millisecond}})
	d.wait("gw in maintenance", func() bool { return d.status("gw") == StatusMaintenance })
	d.set("gw", true)
	time.Sleep(600 * time.Millisecond) // several failing rounds, HARD long ago
	if d.incident("gw") {
		t.Fatal("a failure inside a maintenance window must not open an incident")
	}
	if v := d.e.Maintenance(); len(v) != 1 || !v[0].Open {
		t.Fatalf("maintenance view: %+v", v)
	}
	// Still down when the window closes: notified now.
	d.wait("incident after the window", func() bool { return d.incident("gw") && d.status("gw") == StatusCritical })
	if time.Since(start) < 1500*time.Millisecond {
		t.Fatal("notified before the window closed")
	}
}

func TestMaintenanceRecoveryInsideWindowIsSilent(t *testing.T) {
	d := newDepEnv(t, MaintSpec{Name: "work", Monitors: []string{"gw"},
		Schedule: schedule.Spec{At: time.Now(), Duration: time.Second}})
	d.wait("gw in maintenance", func() bool { return d.status("gw") == StatusMaintenance })
	d.set("gw", true)
	time.Sleep(300 * time.Millisecond)
	d.set("gw", false)
	d.wait("window closed", func() bool { return d.status("gw") == "ok" })
	time.Sleep(300 * time.Millisecond)
	if d.incident("gw") {
		t.Fatal("recovered inside the window: nothing to notify")
	}
}
