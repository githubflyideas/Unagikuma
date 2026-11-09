package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) (string, DB) { return setupFuzz(t) }

func setupFuzz(t testing.TB) (string, DB) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"monitors.conf": "@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n", "unagikuma.key": "KEY\n",
		"cluster-ca.crt": "CA\n", "cluster-ca.key": "CAKEY\n", "cluster.crt": "LEAF\n", "cluster.key": "LEAFKEY\n",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	for _, name := range keyFiles {
		if strings.Contains(name, "/") {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("SITE-TEST-KEY\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	db := func(_ context.Context, which string, w io.Writer) error {
		// Large enough to span several encrypted chunks.
		_, err := io.WriteString(w, which+strings.Repeat("x", 3*chunkSize))
		return err
	}
	return dir, db
}

func write(t *testing.T, dir string, db DB, o Options) []byte { return writeFuzz(t, dir, db, o) }

func writeFuzz(t testing.TB, dir string, db DB, o Options) []byte {
	t.Helper()
	o.DataDir, o.ConfigPath, o.Now = dir, filepath.Join(dir, "monitors.conf"), time.Now()
	var buf bytes.Buffer
	if _, err := Write(context.Background(), &buf, db, o); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPlainBackupLeavesKeysOut(t *testing.T) {
	dir, db := setup(t)
	arch := write(t, dir, db, Options{History: true})
	dst := t.TempDir()
	m, err := Restore(bytes.NewReader(arch), dst, filepath.Join(dst, "monitors.conf"), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if m.Keys || m.Encrypted || strings.Join(m.Files, ",") != "core.db,history.db,monitors.conf" {
		t.Fatalf("manifest: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(dst, "unagikuma.key")); err == nil {
		t.Fatal("a plain backup must not contain the key")
	}
	b, _ := os.ReadFile(filepath.Join(dst, "history.db"))
	if !strings.HasPrefix(string(b), "history") || len(b) != len("history")+3*chunkSize {
		t.Fatal("history.db content")
	}
}

func TestEncryptedBackupRoundTrip(t *testing.T) {
	dir, db := setup(t)
	arch := write(t, dir, db, Options{Passphrase: "correct horse"})
	if bytes.Contains(arch, []byte("CAKEY")) {
		t.Fatal("key material visible in an encrypted archive")
	}
	dst := t.TempDir()
	if _, err := Restore(bytes.NewReader(arch), dst, filepath.Join(dst, "monitors.conf"), "", false); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("no passphrase: %v", err)
	}
	if _, err := Restore(bytes.NewReader(arch), dst, filepath.Join(dst, "monitors.conf"), "wrong", false); err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("wrong passphrase: %v", err)
	}
	m, err := Restore(bytes.NewReader(arch), dst, filepath.Join(dst, "monitors.conf"), "correct horse", false)
	if err != nil || !m.Keys {
		t.Fatalf("%v %+v", err, m)
	}
	for _, f := range keyFiles {
		got, _ := os.ReadFile(filepath.Join(dst, f))
		want, _ := os.ReadFile(filepath.Join(dir, f))
		if !bytes.Equal(got, want) {
			t.Fatalf("%s differs", f)
		}
		if st, _ := os.Stat(filepath.Join(dst, f)); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", f, st.Mode())
		}
	}
	// An existing installation is not replaced without force.
	if _, err := Restore(bytes.NewReader(arch), dst, filepath.Join(dst, "monitors.conf"), "correct horse", false); err == nil {
		t.Fatal("overwrote without force")
	}
	if _, err := Restore(bytes.NewReader(arch), dst, filepath.Join(dst, "monitors.conf"), "correct horse", true); err != nil {
		t.Fatal(err)
	}
}

func TestTamperedBackupsAreRejected(t *testing.T) {
	dir, db := setup(t)
	arch := write(t, dir, db, Options{Passphrase: "p"})
	flip := append([]byte(nil), arch...)
	flip[len(flip)/2] ^= 1
	truncated := arch[:len(arch)-len(arch)/3]
	// Dropping the last chunk must not look like a complete archive.
	for name, b := range map[string][]byte{"flipped": flip, "truncated": truncated} {
		dst := t.TempDir()
		if _, err := Restore(bytes.NewReader(b), dst, filepath.Join(dst, "monitors.conf"), "p", false); err == nil {
			t.Errorf("%s archive accepted", name)
		}
		if _, err := os.Stat(filepath.Join(dst, "core.db")); err == nil {
			t.Errorf("%s archive left files behind", name)
		}
	}
	dst := t.TempDir()
	if _, err := Restore(strings.NewReader("not a backup"), dst, filepath.Join(dst, "monitors.conf"), "", false); err == nil {
		t.Error("garbage accepted")
	}
}

// Encrypted archives written before v1.6 carry an older header and must
// still restore.
func TestLegacyEncryptedHeaderRestores(t *testing.T) {
	dir, db := setup(t)
	arch := write(t, dir, db, Options{Passphrase: "p"})
	if !bytes.HasPrefix(arch, []byte(magic)) {
		t.Fatalf("new header: %q", arch[:len(magic)])
	}
	old := append(append([]byte(nil), legacyMagic...), arch[len(magic):]...)
	dst := t.TempDir()
	if _, err := Restore(bytes.NewReader(old), dst, filepath.Join(dst, "monitors.conf"), "p", false); err != nil {
		t.Fatalf("legacy archive: %v", err)
	}
}

// Theme packs travel with a backup; names outside the pack layout do not.
func TestBackupKeepsThemePacks(t *testing.T) {
	src := t.TempDir()
	cfg := filepath.Join(src, "monitors.conf")
	os.WriteFile(cfg, []byte("; empty\n"), 0o600)
	os.MkdirAll(filepath.Join(src, "themes", "acme"), 0o755)
	os.WriteFile(filepath.Join(src, "themes", "acme", "theme.json"), []byte(`{"name":"Acme"}`), 0o644)
	os.WriteFile(filepath.Join(src, "themes", "acme", "logo"), []byte("png"), 0o644)
	os.WriteFile(filepath.Join(src, "themes", "active"), []byte("acme\n"), 0o644)
	os.WriteFile(filepath.Join(src, "themes", "acme", "notes.txt"), []byte("left out"), 0o644)
	var buf bytes.Buffer
	db := func(ctx context.Context, which string, w io.Writer) error {
		_, err := w.Write([]byte("db"))
		return err
	}
	if _, err := Write(context.Background(), &buf, db, Options{DataDir: src, ConfigPath: cfg, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if _, err := Restore(&buf, dst, filepath.Join(dst, "monitors.conf"), "", false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"themes/acme/theme.json", "themes/acme/logo", "themes/active"} {
		if _, err := os.Stat(filepath.Join(dst, f)); err != nil {
			t.Errorf("%s not restored", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "themes", "acme", "notes.txt")); err == nil {
		t.Error("a file outside the pack layout was backed up")
	}
}

// Archives written before 2.6 name the configuration monitors.bind; it is
// restored to the configuration path all the same.
func TestLegacyConfigEntryRestores(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("; old name\n")
	man := []byte(`{"format":1,"files":["monitors.bind"]}`)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o640, Size: int64(len(man)), Typeflag: tar.TypeReg})
	tw.Write(man)
	tw.WriteHeader(&tar.Header{Name: "monitors.bind", Mode: 0o640, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	dst := t.TempDir()
	cfg := filepath.Join(dst, "monitors.conf")
	if _, err := Restore(&buf, dst, cfg, "", false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(cfg); err != nil || string(b) != string(body) {
		t.Fatalf("restored %q %v", b, err)
	}
}
