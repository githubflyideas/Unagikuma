package server

import (
	"os"
	"strings"
	"testing"
)

const renameBind = testBind + `old     IN CHANNEL log
other   IN CHANNEL log
both    IN CHANNEL group members=old,other
default IN POLICY chain=other,old escalate=30m:old+other
ops     IN TEAM members=admin channels=old
`

func TestChannelRenameFollowsReferences(t *testing.T) {
	e := newEnv(t, renameBind, nil)
	e.setup()
	var pr struct{ ID int64 }
	if c := e.do("POST", "/api/v1/people", map[string]any{"handle": "li", "name": "Li", "emails": []string{"li@example.com"}, "notify": []string{}}, &pr); c != 200 {
		t.Fatalf("person: %d", c)
	}
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "renamed", "from": "old", "kind": "log", "opts": map[string]string{}}, nil); c != 200 {
		t.Fatalf("rename: %d", c)
	}
	cfg := e.app.EffectiveConfig()
	if cfg.FindChannel("old") != nil || cfg.FindChannel("renamed") == nil {
		t.Fatal("channel not renamed")
	}
	b, _ := os.ReadFile(e.app.ConfigPath())
	file := string(b)
	for _, want := range []string{"members=renamed,other", "chain=other,renamed", "escalate=30m:renamed+other", "channels=renamed"} {
		if !strings.Contains(file, want) {
			t.Errorf("%q missing from:\n%s", want, file)
		}
	}
	if strings.Contains(file, "old") && strings.Contains(file, "=old") {
		t.Errorf("old name left in:\n%s", file)
	}
	// Renaming onto a taken name is refused.
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "other", "from": "renamed", "kind": "log", "opts": map[string]string{}}, nil); c == 200 {
		t.Fatal("renamed onto an existing channel")
	}
}
