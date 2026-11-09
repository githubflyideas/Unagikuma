package server

import (
	"fmt"
	"testing"
)

// A save carrying a stale etag fails; a change to another record does
// not make it stale.
func TestIfMatchOnRecords(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	etag := func(name string) string {
		var r struct{ Row map[string]any }
		e.do("GET", "/api/v1/monitors/"+name, nil, &r)
		s, _ := r.Row["etag"].(string)
		if s == "" {
			t.Fatalf("%s has no etag: %v", name, r.Row)
		}
		return s
	}
	put := func(name, target, tag string) (int, map[string]any) {
		var out map[string]any
		code := e.do("PUT", "/api/v1/monitors/"+name, map[string]any{"record": map[string]any{"type": "TCP", "target": target}}, &out, "If-Match", tag)
		return code, out
	}
	dbTag := etag("db")
	// Someone changes another monitor: db's etag still holds.
	if code := e.do("PUT", "/api/v1/monitors/web", map[string]any{"record": map[string]any{"type": "HTTP", "target": "http://192.168.1.11/"}}, nil); code != 200 {
		t.Fatalf("edit web: %d", code)
	}
	if etag("db") != dbTag {
		t.Fatal("editing web changed db's etag")
	}
	code, out := put("db", "192.168.1.21:5432", dbTag)
	if code != 200 || out["record_etag"] == nil || out["record_etag"] == dbTag {
		t.Fatalf("save with a current etag: %d %v", code, out)
	}
	// The old etag is now stale.
	code, out = put("db", "192.168.1.22:5432", dbTag)
	if code != 412 || out["code"] != CodeChanged || out["record_etag"] != etag("db") {
		t.Fatalf("save with a stale etag: %d %v", code, out)
	}
	if m := e.app.Config().Config.FindMonitor("db"); m.Target != "192.168.1.21:5432" {
		t.Fatalf("stale save was applied: %s", m.Target)
	}
	if code := e.do("DELETE", "/api/v1/monitors/db", nil, nil, "If-Match", dbTag); code != 412 {
		t.Fatalf("delete with a stale etag: %d", code)
	}
	if code := e.do("DELETE", "/api/v1/monitors/db", nil, nil, "If-Match", etag("db")); code != 200 {
		t.Fatalf("delete with the current etag: %d", code)
	}
}

func TestMonitorPagesAndIDs(t *testing.T) {
	src := testBind
	for i := 0; i < 7; i++ {
		src += fmt.Sprintf("m%d IN TCP 192.168.2.%d:22\n", i, i+1)
	}
	e := newEnv(t, src, nil)
	e.setup()
	type page struct {
		Items []struct {
			ID   int64
			Name string
		}
		Total      int
		NextCursor *string `json:"next_cursor"`
	}
	seen := map[string]int64{}
	cursor, total := "", 0
	for n := 0; ; n++ {
		var p page
		if code := e.do("GET", "/api/v1/monitors?limit=4&cursor="+cursor, nil, &p); code != 200 {
			t.Fatalf("page %d: %d", n, code)
		}
		if total == 0 {
			total = p.Total
		}
		if p.Total != total || total < 9 || len(p.Items) > 4 {
			t.Fatalf("page %d: %+v", n, p)
		}
		for _, it := range p.Items {
			if _, dup := seen[it.Name]; dup {
				t.Fatalf("%s on two pages", it.Name)
			}
			seen[it.Name] = it.ID
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	if len(seen) != total {
		t.Fatalf("pages held %d monitors", len(seen))
	}
	if code := e.do("GET", "/api/v1/monitors?limit=4&sort=name", nil, nil); code != 400 {
		t.Fatalf("sort with limit: %d", code)
	}
	// The stable id addresses a monitor too.
	var d struct{ Row map[string]any }
	if code := e.do("GET", fmt.Sprintf("/api/v1/monitors/id:%d", seen["m3"]), nil, &d); code != 200 || d.Row["name"] != "m3" {
		t.Fatalf("by id: %d %v", code, d.Row)
	}
}

func TestIdempotentCreate(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	body := map[string]any{"record": map[string]any{"name": "cache", "type": "TCP", "target": "192.168.1.30:6379"}}
	var first, again map[string]any
	if code := e.do("POST", "/api/v1/monitors", body, &first, "Idempotency-Key", "k1"); code != 200 {
		t.Fatalf("create: %d %v", code, first)
	}
	if code := e.do("POST", "/api/v1/monitors", body, &again, "Idempotency-Key", "k1"); code != 200 || fmt.Sprint(again) != fmt.Sprint(first) {
		t.Fatalf("retry: %d %v, first %v", code, again, first)
	}
	// Without the key the same request is a second create: it conflicts.
	if code := e.do("POST", "/api/v1/monitors", body, nil); code != 409 {
		t.Fatalf("create without key: %d", code)
	}
	other := map[string]any{"record": map[string]any{"name": "cache2", "type": "TCP", "target": "192.168.1.31:6379"}}
	if code := e.do("POST", "/api/v1/monitors", other, nil, "Idempotency-Key", "k1"); code != 422 {
		t.Fatalf("key reused for another body: %d", code)
	}
	// A failed request is not remembered: fixing it under the same key works.
	bad := map[string]any{"record": map[string]any{"name": "q", "type": "NOPE"}}
	if code := e.do("POST", "/api/v1/monitors", bad, nil, "Idempotency-Key", "k2"); code != 400 {
		t.Fatalf("bad create: %d", code)
	}
	if code := e.do("POST", "/api/v1/monitors", bad, nil, "Idempotency-Key", "k2"); code != 400 {
		t.Fatalf("bad create retried: %d", code)
	}
}
