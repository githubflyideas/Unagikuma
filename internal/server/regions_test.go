package server

import (
	"strings"
	"testing"
)

const regionsBind = `$FILE revision=2026110901 interval=1h
$SET engine.first-round-delay=0s suspend.recheck-spread=0s
n1      IN NODE region=north
n2      IN NODE
n3      IN NODE region=north
a       IN TCP 10.0.0.1:22 region=north
b       IN TCP 10.0.0.2:22 group=web
$SCOPE region:north
c       IN TCP 10.0.0.3:22
$SCOPE
`

// Regions are the region= of nodes; global is every node. Deleting a
// region moves its monitors to global, and monitors move between groups
// and regions in bulk.
func TestRegionsAndMove(t *testing.T) {
	e := newEnv(t, regionsBind, nil)
	e.setup()
	var list struct {
		Items []struct {
			Name     string
			Nodes    []string
			Monitors int
		}
	}
	e.do("GET", "/api/v1/regions", nil, &list)
	if len(list.Items) != 2 || list.Items[0].Name != "global" || list.Items[0].Monitors != 1 || list.Items[1].Name != "north" || list.Items[1].Monitors != 2 {
		t.Fatalf("regions: %+v", list)
	}
	// A new region from nodes; another node leaves north.
	if c := e.do("POST", "/api/v1/regions", map[string]any{"name": "south", "nodes": []string{"n2"}}, nil); c != 200 {
		t.Fatalf("add region: %d", c)
	}
	if c := e.do("POST", "/api/v1/regions", map[string]any{"name": "global", "nodes": []string{"n2"}}, nil); c != 400 {
		t.Fatalf("region named global: %d", c)
	}
	if c := e.do("POST", "/api/v1/regions", map[string]any{"name": "north", "nodes": []string{"n1"}}, nil); c != 200 {
		t.Fatalf("edit region: %d", c)
	}
	cfg := e.app.Config().Config
	if cfg.FindNode("n2").Region != "south" || cfg.FindNode("n3").Region != "" {
		t.Fatalf("nodes: %+v %+v", cfg.FindNode("n2"), cfg.FindNode("n3"))
	}
	// Move a and b to south and to the db group, in one change.
	var res struct{ Moved int }
	if c := e.do("POST", "/api/v1/monitors/move", map[string]any{"names": []string{"a", "b"}, "group": "db", "region": "south"}, &res); c != 200 || res.Moved != 2 {
		t.Fatalf("move: %d %+v", c, res)
	}
	cfg = e.app.Config().Config
	if a, b := cfg.FindMonitor("a"), cfg.FindMonitor("b"); a.Region != "south" || b.Region != "south" || a.Group != "db" || b.Group != "db" {
		t.Fatalf("moved: %+v %+v", a, b)
	}
	if c := e.do("POST", "/api/v1/monitors/move", map[string]any{"names": []string{"a"}, "region": "nowhere"}, nil); c != 400 {
		t.Fatalf("move to an unknown region: %d", c)
	}
	// Deleting north moves c (written under $SCOPE region:north) to global.
	if c := e.do("DELETE", "/api/v1/regions/north", nil, nil); c != 200 {
		t.Fatalf("delete region: %d", c)
	}
	cfg = e.app.Config().Config
	if m := cfg.FindMonitor("c"); m.Region != "global" || len(m.Nodes) != 0 {
		t.Fatalf("c after delete: %+v", m)
	}
	if !strings.Contains(e.app.Config().Text, "region=global") {
		t.Fatalf("c needs region=global under its $SCOPE:\n%s", e.app.Config().Text)
	}
	// This node lists what it checks itself: c, now global.
	if got := names(t, e, "/api/v1/monitors?q=region:global", "name"); !same(got, "c") {
		t.Fatalf("region:global: %v", got)
	}
}

// A user's groups are kept with the account and listed with it.
func TestUserGroups(t *testing.T) {
	e := newEnv(t, regionsBind, nil)
	e.setup()
	var created struct{ ID int64 }
	if c := e.do("POST", "/api/v1/users", map[string]any{"username": "zhao", "password": "secret123", "role": "viewer", "groups": []string{"web", "db", "web"}}, &created); c != 200 {
		t.Fatalf("create: %d", c)
	}
	groupsOf := func(name string) []string {
		var users struct {
			Items []struct {
				Username string
				Groups   []string
			}
		}
		e.do("GET", "/api/v1/users", nil, &users)
		for _, u := range users.Items {
			if u.Username == name {
				return u.Groups
			}
		}
		return nil
	}
	if got := groupsOf("zhao"); !same(got, "web", "db") {
		t.Fatalf("groups: %v", got)
	}
	if c := e.do("PUT", "/api/v1/users/"+itoa(created.ID), map[string]any{"groups": []string{}}, nil); c != 200 {
		t.Fatalf("update: %d", c)
	}
	if got := groupsOf("zhao"); len(got) != 0 {
		t.Fatalf("groups after clearing: %v", got)
	}
}
