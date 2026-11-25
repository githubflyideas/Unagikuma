package config

import (
	"fmt"
	"testing"
)

// FindMonitor answers like a linear search: after monitors are added or
// the slice is replaced, for duplicates (the first wins) and when two
// configurations are asked about in turn.
func TestFindMonitorIndex(t *testing.T) {
	mk := func(names ...string) *Config {
		c := &Config{}
		for _, n := range names {
			c.Monitors = append(c.Monitors, &Monitor{Name: n})
		}
		return c
	}
	a, b := mk("web", "db", "web"), mk("db", "mail")
	for range 3 { // in turn, so both stay indexed
		if m := a.FindMonitor("web"); m != a.Monitors[0] {
			t.Fatal("duplicate: the first should win")
		}
		if a.FindMonitor("mail") != nil || b.FindMonitor("mail") != b.Monitors[1] || b.FindMonitor("web") != nil {
			t.Fatal("wrong configuration answered")
		}
	}
	a.Monitors = append(a.Monitors, &Monitor{Name: "new"})
	if a.FindMonitor("new") != a.Monitors[3] {
		t.Fatal("added monitor not found")
	}
	a.Monitors = []*Monitor{{Name: "other"}, {Name: "db"}, {Name: "x"}, {Name: "y"}}
	if a.FindMonitor("web") != nil || a.FindMonitor("other") != a.Monitors[0] {
		t.Fatal("replaced slice not reindexed")
	}
	a.Monitors[2].Name = "renamed"
	if a.FindMonitor("x") != nil || a.FindMonitor("renamed") != a.Monitors[2] {
		t.Fatal("a renamed monitor is found under its old name")
	}
	// Many configurations: never a wrong answer.
	var cs []*Config
	for i := range 10 {
		cs = append(cs, mk(fmt.Sprintf("m%d", i)))
	}
	for i, c := range cs {
		if c.FindMonitor(fmt.Sprintf("m%d", i)) != c.Monitors[0] || c.FindMonitor("m99") != nil {
			t.Fatalf("config %d", i)
		}
	}
}
