package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A restore cannot replace the databases of a running server. The web UI
// therefore stages it: the archive is checked and unpacked into a folder
// of the data directory, and the next start of the program puts it in
// place (ApplyPending), keeping what it replaces. Nothing about the
// running installation changes until then.

const (
	pendingDir     = "restore-pending"
	stagedManifest = ".staged.json"
	beforePrefix   = "restore-before-"
	failedPrefix   = "restore-failed-"
	stagingPrefix  = ".restore-staging-"
)

// Staged describes a restore waiting for the next start.
type Staged struct {
	Manifest
	StagedAt time.Time `json:"staged_at"`
}

// Stage checks an archive (and decrypts it with passphrase when it is
// encrypted) and unpacks it for the next start, replacing any restore
// staged before. A damaged archive, a wrong passphrase or an archive
// without a database leaves nothing behind.
func Stage(r io.Reader, dataDir, passphrase string, now time.Time) (Staged, error) {
	return StageChecked(r, dataDir, passphrase, now, nil)
}

// StageChecked is Stage with one more check: check is given the folder the
// archive was unpacked into and can refuse it (a configuration or
// database this version cannot run), which leaves nothing behind either.
func StageChecked(r io.Reader, dataDir, passphrase string, now time.Time, check func(dir string) error) (Staged, error) {
	var st Staged
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return st, err
	}
	tmp, err := os.MkdirTemp(dataDir, stagingPrefix)
	if err != nil {
		return st, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()
	m, err := Restore(r, tmp, filepath.Join(tmp, configEntry), passphrase, true)
	if err != nil {
		return st, err
	}
	if !contains(m.Files, "core.db") {
		return st, errors.New("this backup has no database (core.db): it is not a backup of a whole installation")
	}
	if check != nil {
		if err := check(tmp); err != nil {
			return st, err
		}
	}
	st = Staged{Manifest: m, StagedAt: now}
	b, err := json.Marshal(st)
	if err != nil {
		return st, err
	}
	if err := os.WriteFile(filepath.Join(tmp, stagedManifest), b, 0o600); err != nil {
		return st, err
	}
	final := filepath.Join(dataDir, pendingDir)
	if err := os.RemoveAll(final); err != nil {
		return st, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return st, err
	}
	ok = true
	return st, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Pending reports the restore waiting for the next start, if any.
func Pending(dataDir string) (Staged, bool) {
	var st Staged
	b, err := os.ReadFile(filepath.Join(dataDir, pendingDir, stagedManifest))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, true
}

// Cancel drops the restore waiting for the next start.
func Cancel(dataDir string) error {
	return os.RemoveAll(filepath.Join(dataDir, pendingDir))
}

type move struct{ from, to string }

// ApplyPending puts a staged restore in place. Call it before the data
// directory is opened. What it replaces is moved to restore-before-<time>
// in the data directory, so nothing is lost; if any step fails, everything
// is put back, the staged restore is set aside as restore-failed-<time>
// (so the next start does not try it again) and the error is returned.
// It returns the folder holding the replaced files.
func ApplyPending(dataDir, configPath string, now time.Time) (applied bool, st Staged, before string, err error) {
	st, ok := Pending(dataDir)
	if !ok {
		return false, st, "", nil
	}
	src := filepath.Join(dataDir, pendingDir)
	stamp := now.Format("20060102-150405")
	before = filepath.Join(dataDir, beforePrefix+stamp)
	var aside, placed []move
	defer func() {
		if err == nil {
			return
		}
		for i := len(placed) - 1; i >= 0; i-- { // what was put in place goes back to the staging folder
			os.Rename(placed[i].to, placed[i].from)
		}
		for i := len(aside) - 1; i >= 0; i-- { // and the originals come back
			os.Rename(aside[i].to, aside[i].from)
		}
		os.Remove(before)
		os.Rename(src, filepath.Join(dataDir, failedPrefix+stamp))
		applied = false
	}()
	var files []string
	werr := filepath.WalkDir(src, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() && d.Name() != stagedManifest {
			rel, _ := filepath.Rel(src, p)
			files = append(files, rel)
		}
		return nil
	})
	if werr != nil {
		return false, st, "", werr
	}
	dest := func(rel string) string {
		if rel == configEntry {
			return configPath
		}
		return filepath.Join(dataDir, rel)
	}
	if err = os.MkdirAll(before, 0o700); err != nil {
		return false, st, "", err
	}
	// Move what is there out of the way: the replaced files, and the
	// write-ahead logs of replaced databases, which would otherwise be
	// replayed into the restored one.
	for _, rel := range files {
		d := dest(rel)
		olds := []string{d}
		if strings.HasSuffix(rel, ".db") {
			olds = append(olds, d+"-wal", d+"-shm")
		}
		for _, o := range olds {
			if _, e := os.Lstat(o); e != nil {
				continue
			}
			to := filepath.Join(before, strings.TrimPrefix(filepath.ToSlash(rel), "/")+strings.TrimPrefix(o, d))
			if err = os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
				return false, st, "", err
			}
			if err = os.Rename(o, to); err != nil {
				return false, st, "", fmt.Errorf("moving %s aside: %w", o, err)
			}
			aside = append(aside, move{o, to})
		}
	}
	for _, rel := range files {
		from, to := filepath.Join(src, rel), dest(rel)
		if err = os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return false, st, "", err
		}
		if err = os.Rename(from, to); err != nil {
			return false, st, "", fmt.Errorf("putting %s in place: %w", to, err)
		}
		placed = append(placed, move{from, to})
	}
	os.RemoveAll(src)
	// Nothing was replaced when the folder stayed empty (a first install).
	if entries, e := os.ReadDir(before); e == nil && len(entries) == 0 {
		os.Remove(before)
		before = ""
	}
	return true, st, before, nil
}
