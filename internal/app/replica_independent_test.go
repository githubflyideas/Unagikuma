package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplicaConfigIsAlwaysLocal(t *testing.T) {
	text := testConfig + `bj IN NODE "Beijing"
`
	a, _, _, dir := newApp(t, text)
	defer a.store.Close()
	if err := os.WriteFile(filepath.Join(dir, agentFile), mustJSON(Membership{Node: "bj", Server: "https://offline.invalid"}), 0600); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, replicaDir), 0700)
	if err := os.WriteFile(filepath.Join(dir, replicaDir, replicaConfig), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	a.loadMembership()
	a.loadReplica()
	ec, ok := a.localEngineConfig()
	if !ok || ec.Self != "bj" || len(ec.Monitors) == 0 {
		t.Fatalf("not independently scheduled: %+v %v", ec, ok)
	}
	for _, m := range ec.Monitors {
		if len(m.Nodes) != 0 {
			t.Fatal("remote round remains", m.Name)
		}
	}
}
