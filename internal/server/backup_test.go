package server

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/backup"
)

func TestBackupDownloads(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	fetch := func(method, body string) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, e.ts.URL+"/api/v1/backup?history=1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Unagikuma-CSRF", e.csrf)
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", method, resp.StatusCode, b)
		}
		return b
	}
	plain := fetch("GET", "")
	dst := t.TempDir()
	m, err := backup.Restore(bytes.NewReader(plain), dst, filepath.Join(dst, "monitors.conf"), "", false)
	if err != nil || m.Keys || len(m.Files) != 4 { // databases, monitors.conf, installation number
		t.Fatalf("plain: %v %+v", err, m)
	}
	enc := fetch("POST", `{"passphrase":"long enough"}`)
	dst = t.TempDir()
	m, err = backup.Restore(bytes.NewReader(enc), dst, filepath.Join(dst, "monitors.conf"), "long enough", false)
	if err != nil || !m.Keys {
		t.Fatalf("encrypted: %v %+v", err, m)
	}
	if !strings.Contains(strings.Join(m.Files, ","), "cluster-ca.key") || !strings.Contains(strings.Join(m.Files, ","), "unagikuma.key") {
		t.Fatalf("keys missing: %v", m.Files)
	}
	if c := e.do("POST", "/api/v1/backup", map[string]any{"passphrase": "short"}, nil); c != 400 {
		t.Fatalf("short passphrase: %d", c)
	}
}
