package server

import (
	"strings"
	"testing"
	"time"
)

func TestMaintenanceAPI(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	start := time.Now().Add(-time.Minute).Format("2006-01-02 15:04")
	body := map[string]any{"name": "upgrade", "title": "Core switch upgrade", "select": "group:public", "at": start, "duration": "2h", "notify": true}
	if c := e.do("POST", "/api/v1/maintenance", body, nil); c != 200 {
		t.Fatalf("create: %d", c)
	}
	var list struct {
		Items []maintJSON `json:"items"`
	}
	if e.do("GET", "/api/v1/maintenance", nil, &list) != 200 || len(list.Items) != 1 {
		t.Fatalf("list: %+v", list)
	}
	m := list.Items[0]
	if !m.Open || m.Select != "group:public" || !m.Notify || strings.Join(m.Monitors, ",") != "web" || !strings.Contains(m.Schedule, "at=") {
		t.Fatalf("window: %+v", m)
	}
	waitUntil(t, func() bool { r, _ := e.app.Engine().Row("web"); return r.Status == "maintenance" })

	// Invalid windows are refused with the configuration's diagnostics.
	if c := e.do("POST", "/api/v1/maintenance", map[string]any{"name": "bad", "select": "status:critical", "at": start, "duration": "1h"}, nil); c != 400 {
		t.Fatalf("invalid select: %d", c)
	}
	if c := e.do("POST", "/api/v1/maintenance", map[string]any{"name": "web", "select": "name:web", "at": start, "duration": "1h"}, nil); c != 409 {
		t.Fatalf("name clash: %d", c)
	}
	if e.do("DELETE", "/api/v1/maintenance/upgrade", nil, nil) != 200 || len(e.app.Config().Config.Maints) != 0 {
		t.Fatal("delete")
	}
}
