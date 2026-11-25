package app

import (
	"encoding/json"
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPromotePreservesLocalRuntime(t *testing.T) {
	src, _, _, _ := newApp(t, testConfig)
	defer src.store.Close()
	dst, _, _, dir := newApp(t, testConfig)
	inc := &engine.Incident{Monitor: "web", Title: "only-local", State: engine.IncidentState{OpenedAt: time.Now()}}
	if err := dst.store.SaveIncident(inc); err != nil {
		t.Fatal(err)
	}
	if err := dst.store.Audit(store.AuditEntry{Actor: "local", Action: "test", Target: "incident"}); err != nil {
		t.Fatal(err)
	}
	if err := dst.store.SetSetting("runtime-sentinel", "preserved"); err != nil {
		t.Fatal(err)
	}
	if err := dst.secrets.Put("local-only", "secret", nil); err != nil {
		t.Fatal(err)
	}
	before, err := dst.store.VerifyAudit()
	if err != nil || !before.OK {
		t.Fatal(before, err)
	}
	dst.store.Close()
	rec := httptest.NewRecorder()
	if err := src.writeState(rec); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, standbyDir), 0700)
	os.WriteFile(filepath.Join(dir, standbyDir, stateFile), rec.Body.Bytes(), 0600)
	meta := StandbyState{Key: rec.Header().Get(cluster.StateKeyHeader), Term: 1}
	b, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, standbyDir, stateMetaFile), b, 0600)
	b, _ = json.Marshal(promoteRequest{Term: 2})
	os.WriteFile(filepath.Join(dir, promoteFile), b, 0600)
	ok, err := applyPromote(dir, filepath.Join(dir, "monitors.conf"))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	st, err := store.Open(dir, store.Options{Tool: true})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.Incident(inc.ID)
	if err != nil || got == nil || got.Title != "only-local" {
		t.Fatalf("incident=%+v %v", got, err)
	}
	if v, _ := st.Setting("runtime-sentinel"); v != "preserved" {
		t.Fatal("settings lost")
	}
	if after, err := st.VerifyAudit(); err != nil || !after.OK || after.Head != before.Head || after.Entries != before.Entries {
		t.Fatal("audit changed", before, after, err)
	}
	key, err := store.LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Secrets(key).Get("local-only"); v != "secret" {
		t.Fatal("local encryption key overwritten")
	}
}
