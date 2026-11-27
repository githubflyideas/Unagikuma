package hostview

import (
	"reflect"
	"testing"
)

func TestBuildGroupsByHostAndSorts(t *testing.T) {
	hosts := Build([]Service{
		{Name: "web-http", Host: "web", Address: "", Area: "a", Group: "ops", Status: "ok"},
		{Name: "db-ping", Host: "db", Address: "10.0.0.2", Area: "a", Ping: true, Status: "ok"},
		{Name: "web-ping", Host: "web", Address: "10.0.0.1", Area: "b", Ping: true, Status: "critical"},
		{Name: "web-tls", Host: "web", Status: ""},
	})
	if len(hosts) != 2 || hosts[0].Name != "db" || hosts[1].Name != "web" {
		t.Fatalf("hosts: %+v", hosts)
	}
	w := hosts[1]
	if w.Address != "10.0.0.1" || w.Check != "web-ping" || w.Status != "down" || w.Worst != "critical" || w.Total != 3 {
		t.Fatalf("web: %+v", w)
	}
	if w.Area != "" || w.Group != "ops" {
		t.Fatalf("services in two sections keep no area; the first group stays: %+v", w)
	}
	if want := map[string]int{"ok": 1, "critical": 1, "pending": 1}; !reflect.DeepEqual(w.Services, want) {
		t.Fatalf("counts: %v", w.Services)
	}
	if !reflect.DeepEqual(w.Monitors, []string{"web-http", "web-ping", "web-tls"}) {
		t.Fatalf("monitors: %v", w.Monitors)
	}
	if hosts[0].Status != "up" || hosts[0].Area != "a" {
		t.Fatalf("db: %+v", hosts[0])
	}
}

// Without a ping the host is down only when every service is down.
func TestStateWithoutPing(t *testing.T) {
	for _, c := range []struct {
		statuses []string
		want     string
	}{
		{[]string{"critical", "critical"}, "down"},
		{[]string{"critical", "ok"}, "up"},
		{[]string{"unreachable", "critical"}, "unreachable"},
		{[]string{"pending", ""}, "pending"},
		{[]string{"maintenance"}, "maintenance"},
		{[]string{"paused", "paused"}, "paused"},
		{[]string{"degraded"}, "up"},
	} {
		var svcs []Service
		for i, st := range c.statuses {
			svcs = append(svcs, Service{Name: string(rune('a' + i)), Host: "h", Status: st})
		}
		if got := Build(svcs)[0].Status; got != c.want {
			t.Errorf("%v: %s, want %s", c.statuses, got, c.want)
		}
	}
}

// With a ping, the ping alone decides.
func TestStateFromPing(t *testing.T) {
	for ping, want := range map[string]string{"ok": "up", "degraded": "up", "critical": "down", "": "pending", "paused": "paused", "maintenance": "maintenance", "unreachable": "unreachable"} {
		got := Build([]Service{
			{Name: "svc", Host: "h", Status: "critical"},
			{Name: "ping", Host: "h", Ping: true, Status: ping},
		})[0].Status
		if got != want {
			t.Errorf("ping %q: %s, want %s", ping, got, want)
		}
	}
}
