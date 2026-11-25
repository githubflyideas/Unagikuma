package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const changesBind = `@ IN SOA unagikuma. admin.localhost. ( 2026102801 1m 15s 5s 2 )
ops     IN CHANNEL log
default IN POLICY chain=ops
web     IN HTTP http://192.168.1.10/ group=public
main    IN STATUSPAGE "Service status" select="group:public"
`

func TestChangeAPI(t *testing.T) {
	e := newEnv(t, changesBind, nil)
	e.setup()
	start := time.Now().Add(48 * time.Hour).Truncate(time.Minute)
	var c struct {
		ID             int64
		State          string
		Monitors       int
		AnnouncementID int64 `json:"announcement_id"`
	}
	body := map[string]any{"title": "Core switch upgrade", "impact": "VPN down for 10 minutes", "select": "group:public",
		"status_page": "main", "start": start, "end": start.Add(2 * time.Hour)}
	if code := e.do("POST", "/api/v1/changes", body, &c); code != 200 || c.Monitors != 1 || c.AnnouncementID == 0 || c.State != "planned" {
		t.Fatalf("create: %d %+v", code, c)
	}
	if code := e.do("POST", "/api/v1/changes", map[string]any{"title": "x", "start": start, "end": start}, nil); code != 400 {
		t.Fatalf("end before start: %d", code)
	}
	// The status page shows it as maintenance.
	var page struct {
		Announcements []struct{ Title, Severity string }
	}
	e.do("GET", "/api/v1/status/main", nil, &page)
	if len(page.Announcements) != 1 || page.Announcements[0].Severity != "maintenance" {
		t.Fatalf("status page: %+v", page)
	}
	// The window is in the maintenance list.
	var maint struct{ Items []struct{ Name string } }
	e.do("GET", "/api/v1/maintenance", nil, &maint)
	if len(maint.Items) != 1 || maint.Items[0].Name != fmt.Sprintf("change-%d", c.ID) {
		t.Fatalf("maintenance: %+v", maint)
	}
	// A replaced template is used for the receipt.
	if code := e.do("PUT", "/api/v1/changes/templates", map[string]any{"done": map[string]string{"title": "DONE {{.Title}}", "body": "{{.Note}}"}}, nil); code != 200 {
		t.Fatalf("templates: %d", code)
	}
	if code := e.do("PUT", "/api/v1/changes/templates", map[string]any{"done": map[string]string{"title": "{{.Title"}}, nil); code != 400 {
		t.Fatalf("broken template: %d", code)
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/changes/%d/finish", c.ID), map[string]any{"state": "cancelled", "note": "postponed"}, &c); code != 200 || c.State != "cancelled" {
		t.Fatalf("cancel: %d %+v", code, c)
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/changes/%d/finish", c.ID), map[string]any{"state": "done"}, nil); code != 409 {
		t.Fatalf("finish twice: %d", code)
	}
	e.do("GET", "/api/v1/maintenance", nil, &maint)
	if len(maint.Items) != 0 {
		t.Fatalf("window kept after cancelling: %+v", maint)
	}
	// History: search and CSV.
	var list struct{ Items []struct{ Title string } }
	e.do("GET", "/api/v1/changes?q=switch", nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("search: %+v", list)
	}
	e.do("GET", "/api/v1/changes?q=nothing-like-this", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("search miss: %+v", list)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/changes?format=csv", nil)
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	csv, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(string(csv), "id,title,state") || !strings.Contains(string(csv), "Core switch upgrade,cancelled") {
		t.Fatalf("csv:\n%s", csv)
	}
	// Both notices (announced, cancelled) went out through the default policy.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n struct {
			Items []struct {
				Kind string
				OK   bool
			}
		}
		e.do("GET", "/api/v1/notifications", nil, &n)
		ok := 0
		for _, it := range n.Items {
			if it.Kind == "CHANGE" && it.OK {
				ok++
			}
		}
		if ok == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("notifications: %+v", n.Items)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
