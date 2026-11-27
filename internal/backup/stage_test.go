package backup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// installation makes a data directory that already has databases, a key
// and a configuration, with the given marker in each.
func installation(t *testing.T, marker string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	for _, n := range []string{"core.db", "history.db", "unagikuma.key", "monitors.conf", "core.db-wal"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(marker+" "+n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A staged restore changes nothing until the next start; then it replaces
// the databases, key and configuration, keeps what it replaced, drops the
// old write-ahead logs and leaves nothing to apply a second time.
func TestStageThenApplyAtStart(t *testing.T) {
	src, db := setup(t) // the installation the backup comes from
	archive := write(t, src, db, Options{Passphrase: "long enough pass", History: true})
	dst := installation(t, "OLD")
	conf := filepath.Join(dst, "monitors.conf")

	st, err := Stage(bytes.NewReader(archive), dst, "long enough pass", time.Now())
	if err != nil || !st.Keys || !st.Encrypted {
		t.Fatalf("stage: %v %+v", err, st)
	}
	if got, ok := Pending(dst); !ok || got.Version != st.Version {
		t.Fatalf("pending: %v %+v", ok, got)
	}
	// Still the old installation.
	for _, n := range []string{"core.db", "history.db", "unagikuma.key", "monitors.conf"} {
		if got := readFile(t, filepath.Join(dst, n)); got != "OLD "+n {
			t.Fatalf("%s changed before the restart: %q", n, got)
		}
	}

	applied, _, before, err := ApplyPending(dst, conf, time.Now())
	if err != nil || !applied || before == "" {
		t.Fatalf("apply: %v %v %q", err, applied, before)
	}
	if got := readFile(t, filepath.Join(dst, "core.db")); !strings.HasPrefix(got, "core") || strings.Contains(got, "OLD") {
		t.Fatalf("core.db after the restore: %q", got[:10])
	}
	if got := readFile(t, filepath.Join(dst, "unagikuma.key")); got != "KEY\n" {
		t.Fatalf("key: %q", got)
	}
	if got := readFile(t, conf); !strings.Contains(got, "SOA") {
		t.Fatalf("configuration: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "core.db-wal")); err == nil {
		t.Fatal("the old write-ahead log was left to be replayed into the restored database")
	}
	if _, err := os.Stat(filepath.Join(dst, ".db-epoch-reset")); err != nil {
		t.Fatal("the restored database is not marked for its epoch to rotate")
	}
	// What was replaced is kept.
	for n, want := range map[string]string{"core.db": "OLD core.db", "unagikuma.key": "OLD unagikuma.key", "monitors.conf": "OLD monitors.conf", "core.db-wal": "OLD core.db-wal"} {
		if got := readFile(t, filepath.Join(before, n)); got != want {
			t.Errorf("kept %s: %q", n, got)
		}
	}
	if _, ok := Pending(dst); ok {
		t.Fatal("still pending after it was applied")
	}
	if again, _, _, err := ApplyPending(dst, conf, time.Now()); again || err != nil {
		t.Fatalf("applied twice: %v %v", again, err)
	}
}

func TestStageRefusesAndLeavesNothing(t *testing.T) {
	src, db := setup(t)
	enc := write(t, src, db, Options{Passphrase: "long enough pass"})
	plain := write(t, src, db, Options{})
	dst := installation(t, "OLD")
	before := dirNames(t, dst)
	for name, c := range map[string]struct {
		data []byte
		pass string
	}{
		"wrong passphrase": {enc, "another passphrase"},
		"no passphrase":    {enc, ""},
		"not a backup":     {[]byte("hello, this is not an archive"), ""},
		"truncated":        {plain[:len(plain)/2], ""},
		"empty":            {nil, ""},
	} {
		if _, err := Stage(bytes.NewReader(c.data), dst, c.pass, time.Now()); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if got := dirNames(t, dst); got != before {
			t.Errorf("%s: left something behind: %s", name, got)
		}
	}
	// A plain archive stages too (it has no keys).
	if st, err := Stage(bytes.NewReader(plain), dst, "", time.Now()); err != nil || st.Keys {
		t.Fatalf("plain: %v %+v", err, st)
	}
	// A second one replaces the first; cancelling drops it.
	if _, err := Stage(bytes.NewReader(enc), dst, "long enough pass", time.Now()); err != nil {
		t.Fatal(err)
	}
	if st, ok := Pending(dst); !ok || !st.Keys {
		t.Fatalf("the second did not replace the first: %v %+v", ok, st)
	}
	if err := Cancel(dst); err != nil {
		t.Fatal(err)
	}
	if _, ok := Pending(dst); ok {
		t.Fatal("still pending after cancel")
	}
	if got := dirNames(t, dst); got != before {
		t.Errorf("cancel left something behind: %s", got)
	}
}

func dirNames(t *testing.T, dir string) string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// If a file cannot be put in place, every file goes back, and the staged
// restore is set aside so that the next start does not try it again.
func TestApplyFailureRestoresEverything(t *testing.T) {
	src, db := setup(t)
	archive := write(t, src, db, Options{Passphrase: "long enough pass"})
	dst := installation(t, "OLD")
	// The cluster CA of the archive goes into site-identity/: make that a
	// file, so its directory cannot be created.
	if err := os.WriteFile(filepath.Join(dst, "site-identity"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(bytes.NewReader(archive), dst, "long enough pass", time.Now()); err != nil {
		t.Fatal(err)
	}
	applied, _, _, err := ApplyPending(dst, filepath.Join(dst, "monitors.conf"), time.Now())
	if err == nil || applied {
		t.Fatalf("expected a failure: %v %v", err, applied)
	}
	for _, n := range []string{"core.db", "history.db", "unagikuma.key", "monitors.conf", "core.db-wal"} {
		if got := readFile(t, filepath.Join(dst, n)); got != "OLD "+n {
			t.Errorf("%s not put back: %q", n, got)
		}
	}
	if _, ok := Pending(dst); ok {
		t.Error("the failed restore would be tried again")
	}
	if names := dirNames(t, dst); !strings.Contains(names, failedPrefix) {
		t.Errorf("the staged restore was not set aside: %s", names)
	}
}

// A check that refuses the unpacked archive leaves nothing staged and the
// installation as it was.
func TestStageCheckRefuses(t *testing.T) {
	src, db := setup(t)
	archive := write(t, src, db, Options{Passphrase: "long enough pass"})
	dst := installation(t, "OLD")
	_, err := StageChecked(bytes.NewReader(archive), dst, "long enough pass", time.Now(), func(dir string) error {
		if _, e := os.Stat(filepath.Join(dir, "core.db")); e != nil {
			t.Errorf("the check should see the unpacked files: %v", e)
		}
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("a refused archive was staged")
	}
	if _, ok := Pending(dst); ok {
		t.Fatal("a restore is pending after the refusal")
	}
	if got := readFile(t, filepath.Join(dst, "core.db")); got != "OLD core.db" {
		t.Fatalf("core.db changed: %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dst, ".restore-staging-*")); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

// Stage is StageChecked without the extra check.
func Stage(r io.Reader, dataDir, passphrase string, now time.Time) (Staged, error) {
	return StageChecked(r, dataDir, passphrase, now, nil)
}
