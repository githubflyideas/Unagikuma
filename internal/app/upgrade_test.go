package app

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Data directories left by earlier releases cannot be opened: there is
// no upgrade path from a pre-3.0.1 schema. Start with a fresh directory.
func TestUpgradeFromEarlierReleases(t *testing.T) {
	fixtures, _ := filepath.Glob("testdata/upgrade/*.tar.gz")
	if len(fixtures) < 3 {
		t.Fatalf("fixtures: %v", fixtures)
	}
	for _, fx := range fixtures {
		t.Run(strings.TrimSuffix(filepath.Base(fx), ".tar.gz"), func(t *testing.T) {
			dir := t.TempDir()
			untar(t, fx, dir)
			_, err := New(Options{DataDir: dir, Log: slog.New(slog.DiscardHandler), Instance: "upgrade"})
			if err == nil {
				t.Fatal("opened a pre-3.0.1 data directory")
			}
			msg := err.Error()
			if !strings.Contains(msg, "before 3.0.1") || !strings.Contains(msg, "fresh data directory") {
				t.Fatalf("got %v", err)
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
