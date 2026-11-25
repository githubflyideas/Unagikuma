package backup

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A restore reads a file someone hands you: it must not crash, and it must
// never write outside the data directory or anything but the known files.
func FuzzRestore(f *testing.F) {
	dir, _ := setupFuzz(f)
	// Small databases keep each run fast; the cipher seed costs an
	// argon2 derivation per run, so it comes last and alone.
	small := func(_ context.Context, which string, w io.Writer) error {
		_, err := io.WriteString(w, which)
		return err
	}
	f.Add(writeFuzz(f, dir, small, Options{History: true}))
	f.Add([]byte("garbage"))
	f.Add(writeFuzz(f, dir, small, Options{Passphrase: "p"}))
	f.Fuzz(func(t *testing.T, b []byte) {
		root := t.TempDir()
		data := filepath.Join(root, "data")
		Restore(bytes.NewReader(b), data, filepath.Join(data, "monitors.conf"), "p", false)
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.Name() != "data" {
				t.Fatalf("wrote %s outside the data directory", e.Name())
			}
		}
		files, _ := os.ReadDir(data)
		for _, e := range files {
			ok := e.Name() == "core.db" || e.Name() == "history.db" || e.Name() == "monitors.conf" || e.Name() == ".db-epoch-reset"
			for _, k := range keyFiles {
				ok = ok || e.Name() == k || (e.IsDir() && e.Name() == "site-identity" && strings.HasPrefix(k, "site-identity/"))
			}
			if e.Name() == "site-identity" && e.IsDir() {
				nested, _ := os.ReadDir(filepath.Join(data, e.Name()))
				for _, n := range nested {
					allowed := false
					for _, k := range keyFiles {
						allowed = allowed || k == "site-identity/"+n.Name()
					}
					if !allowed || n.IsDir() {
						t.Fatalf("unexpected identity entry %s", n.Name())
					}
				}
			}
			if !ok {
				t.Fatalf("restore left %s", e.Name())
			}
		}
	})
}
