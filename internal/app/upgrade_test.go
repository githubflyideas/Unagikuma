package app

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Data directories left by earlier releases (scripts/upgrade-fixture.sh)
// open with this one: accounts log in, secrets decrypt, archived incidents,
// push and node tokens survive, and the history moves to the id-keyed
// tables.
func TestUpgradeFromEarlierReleases(t *testing.T) {
	fixtures, _ := filepath.Glob("testdata/upgrade/*.tar.gz")
	if len(fixtures) < 3 {
		t.Fatalf("fixtures: %v", fixtures)
	}
	for _, fx := range fixtures {
		t.Run(strings.TrimSuffix(filepath.Base(fx), ".tar.gz"), func(t *testing.T) {
			dir := t.TempDir()
			untar(t, fx, dir)
			var meta struct {
				Version   string `json:"version"`
				PushToken string `json:"push_token"`
			}
			b, _ := os.ReadFile(filepath.Join(dir, "fixture.json"))
			if err := json.Unmarshal(b, &meta); err != nil {
				t.Fatal(err)
			}
			fp := &fakeProbes{fail: map[string]bool{}}
			a, err := New(Options{DataDir: dir, Probe: fp.run, Log: slog.New(slog.DiscardHandler), Instance: "upgrade"})
			if err != nil {
				t.Fatalf("%s does not open: %v", meta.Version, err)
			}
			defer a.store.Close()
			if _, err := os.Stat(filepath.Join(dir, "unagikuma.key")); err != nil {
				t.Fatal("secret key not carried over")
			}
			if _, err := a.store.Authenticate("admin", "fixture-pass-1"); err != nil {
				t.Fatalf("admin login: %v", err)
			}
			if v, err := a.secrets.Get("ch_ops_url"); err != nil || !strings.HasSuffix(v, "fixture-secret") {
				t.Fatalf("channel secret: %q %v", v, err)
			}
			if name, err := a.store.MonitorByPushToken(meta.PushToken); err != nil || name != "backup" {
				t.Fatalf("push token: %q %v", name, err)
			}
			if toks, _ := a.store.AgentTokens(); len(toks) != 1 || toks[0].Node != "bj" {
				t.Fatalf("node token: %+v", toks)
			}
			history, err := a.store.Incidents("all", "down", 0, 100)
			if err != nil || len(history.Items) != 1 {
				t.Fatalf("legacy history lost: %+v %v", history, err)
			}
			legacy := history.Items[0]
			if !legacy.State.Closed || !legacy.State.ResolvedAt.IsZero() || legacy.GlobalID == "" || !legacy.HistoryOnly {
				t.Fatalf("legacy cluster verdict not safely archived: %+v", legacy)
			}
			for _, m := range []string{"self", "down", "backup"} {
				if a.store.MonitorID(m) == 0 {
					t.Fatalf("no id for %s", m)
				}
			}
			// The history before the upgrade is still there.
			u, err := a.store.UptimeOf("down", time.Now().Add(-400*24*time.Hour), time.Now())
			if err != nil || u.Ratio == nil || u.DownS == 0 {
				t.Fatalf("history of %s lost: %+v %v", meta.Version, u, err)
			}
		})
	}
}

func untar(t *testing.T, path, dir string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, filepath.Clean(h.Name))
		if h.Typeflag == tar.TypeDir {
			os.MkdirAll(p, 0o700)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o700)
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(out, tr)
		out.Close()
	}
}
