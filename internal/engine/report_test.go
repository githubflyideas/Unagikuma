package engine

import (
	"strings"
	"testing"
)

func TestDailyReport(t *testing.T) {
	d := newDepEnv(t)
	d.wait("both ok", func() bool { return d.status("gw") == "ok" && d.status("srv") == "ok" })
	if r := d.e.DailyReport(); !r.OK || !strings.Contains(r.Title, "2") || !strings.Contains(r.Body, "gw, srv") {
		t.Fatalf("all up: %+v", r)
	}
	d.set("gw", true)
	d.set("srv", true)
	d.wait("gw down", func() bool { return d.status("gw") == StatusCritical && d.status("srv") == StatusUnreachable })
	r := d.e.DailyReport()
	if r.OK || r.Policy != "system" {
		t.Fatalf("report: %+v", r)
	}
	for _, want := range []string{"DOWN (1): gw", "UNREACHABLE (1): srv", "CRITICAL", "Services not OK (2)"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, r.Body)
		}
	}
	// The critical service is listed before the unreachable one.
	if strings.Index(r.Body, "• gw") > strings.Index(r.Body, "• srv") {
		t.Errorf("order:\n%s", r.Body)
	}
}
