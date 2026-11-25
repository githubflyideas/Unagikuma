package config

import (
	"errors"
	"os"
	"path/filepath"
)

// FileName is the name of the configuration file in the data directory.
const FileName = "monitors.conf"

// LegacyFileName is the name releases before 2.6 used. It is still read,
// and the server renames it to FileName when it starts.
const LegacyFileName = "monitors.bind"

// PathIn returns the configuration file of a data directory: FileName,
// or the legacy name when only that one exists.
func PathIn(dir string) string {
	p := filepath.Join(dir, FileName)
	if _, err := os.Stat(p); err != nil {
		if old := filepath.Join(dir, LegacyFileName); fileExists(old) {
			return old
		}
	}
	return p
}

// MigrateFileName renames old to new when only old exists. It reports
// whether it renamed anything.
func MigrateFileName(old, new string) (bool, error) {
	if fileExists(new) || !fileExists(old) {
		return false, nil
	}
	if err := os.Rename(old, new); err != nil {
		return false, err
	}
	return true, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
