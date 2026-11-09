package server

import "testing"

// While a monitor is being written, the form learns which monitors check
// exactly the same thing and how many others watch that host; the
// monitor being edited does not count, and a half-written one finds none.
func TestSameCheckLookup(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	type res struct {
		Same []struct{ Name, Title string }
		Host int
	}
	ask := func(typ, target string, opts map[string]string, exclude string) res {
		t.Helper()
		var out res
		body := map[string]any{"record": map[string]any{"name": "x", "type": typ, "target": target, "opts": opts}, "exclude": exclude}
		if code := e.do("POST", "/api/v1/monitors/same-check", body, &out); code != 200 {
			t.Fatalf("same-check: %d", code)
		}
		return out
	}
	if r := ask("TCP", "192.168.1.20:5432", nil, ""); len(r.Same) != 1 || r.Same[0].Name != "db" {
		t.Fatalf("same TCP check: %+v", r)
	}
	if r := ask("TCP", "192.168.1.20:5432", map[string]string{"share": "yes", "title": "DB for team B"}, ""); len(r.Same) != 1 {
		t.Fatalf("share= and a title make no other check: %+v", r)
	}
	if r := ask("TCP", "192.168.1.20:22", nil, ""); len(r.Same) != 0 || r.Host != 1 {
		t.Fatalf("same host, other port: %+v", r)
	}
	if r := ask("HTTP", "http://192.168.1.10/", nil, ""); len(r.Same) != 1 || r.Same[0].Title != "Web site" {
		t.Fatalf("same HTTP check: %+v", r)
	}
	if r := ask("TCP", "192.168.1.20:5432", nil, "db"); len(r.Same) != 0 {
		t.Fatalf("the monitor being edited: %+v", r)
	}
	if r := ask("TCP", "192.168.1.20:", nil, ""); len(r.Same) != 0 || r.Host != 0 {
		t.Fatalf("half written: %+v", r)
	}
}
