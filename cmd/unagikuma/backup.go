package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/backup"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// readPassphrase takes the passphrase from a file or UNAGIKUMA_BACKUP_PASSPHRASE.
func readPassphrase(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return os.Getenv("UNAGIKUMA_BACKUP_PASSPHRASE"), nil
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "data directory")
	cfgPath := fs.String("config", "", "path of monitors.conf (default <data>/monitors.conf)")
	out := fs.String("out", "", "archive to write (default unagikuma-<time>.tar.gz or .ukbackup)")
	history := fs.Bool("history", false, "include history.db (check results; can be large)")
	passFile := fs.String("passphrase-file", "", "encrypt with this passphrase and include the secret key and cluster CA (or set UNAGIKUMA_BACKUP_PASSPHRASE)")
	_ = fs.Parse(args)
	pass, err := readPassphrase(*passFile)
	if err != nil {
		return err
	}
	if pass != "" && len(pass) < 8 {
		return errors.New("the passphrase must have at least 8 characters")
	}
	if *cfgPath == "" {
		*cfgPath = config.PathIn(*data)
	}
	now := time.Now()
	if *out == "" {
		ext := "tar.gz"
		if pass != "" {
			ext = "ukbackup"
		}
		*out = fmt.Sprintf("unagikuma-%s.%s", now.Format("20060102-150405"), ext)
	}
	st, err := store.Open(*data, store.Options{Tool: true})
	if err != nil {
		return err
	}
	defer st.Close()
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	m, err := backup.Write(context.Background(), f, st.BackupDB, backup.Options{
		DataDir: *data, ConfigPath: *cfgPath, History: *history, Passphrase: pass, Version: version, Now: now,
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(*out)
		return err
	}
	cliAudit(st, "backup.create", *out, map[string]any{"encrypted": pass != "", "history": *history})
	fmt.Printf("%s: %s\n", *out, strings.Join(m.Files, ", "))
	if !m.Keys {
		fmt.Println("Not included: unagikuma.key and the cluster CA. Keep them separately, or back up with --passphrase-file.")
	}
	return nil
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "data directory to restore into")
	cfgPath := fs.String("config", "", "where to write monitors.conf (default <data>/monitors.conf)")
	passFile := fs.String("passphrase-file", "", "passphrase of an encrypted backup (or set UNAGIKUMA_BACKUP_PASSPHRASE)")
	force := fs.Bool("force", false, "replace an existing installation (stop the server first)")
	_ = fs.Parse(args)
	// Flags may also follow the archive: "restore x.ukbackup --data DIR".
	if rest := fs.Args(); len(rest) > 1 {
		_ = fs.Parse(rest[1:])
		_ = fs.Parse(append([]string{"--"}, append([]string{rest[0]}, fs.Args()...)...))
	}
	if fs.NArg() != 1 {
		return errors.New("usage: unagikuma restore [--data DIR] [--passphrase-file F] [--force] ARCHIVE")
	}
	pass, err := readPassphrase(*passFile)
	if err != nil {
		return err
	}
	if *cfgPath == "" {
		*cfgPath = config.PathIn(*data)
	}
	if !*force {
		for _, p := range []string{filepath.Join(*data, "core.db"), *cfgPath} {
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s exists; stop the server and use --force to replace it", p)
			}
		}
	}
	// Not under a running server: it would keep writing the files this
	// replaces.
	if err := os.MkdirAll(*data, 0o700); err != nil {
		return err
	}
	if ok, err := lockDataDir(filepath.Join(*data, lockFile)); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("unagikuma is running on %s: stop it first", *data)
	}
	defer closeDataLock()
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	// The same way as a restore from the web UI: unpacked beside the data
	// and checked (databases this version can open, a configuration
	// without errors), then put in place with the replaced files kept.
	now := time.Now()
	st, err := backup.StageChecked(f, *data, pass, now, func(dir string) error { return app.CheckRestore(dir, checkOptions()) })
	if err != nil {
		return err
	}
	applied, _, before, err := backup.ApplyPending(*data, *cfgPath, now)
	if err != nil {
		return fmt.Errorf("the backup could not be put in place, so the installation is as it was: %w", err)
	}
	if !applied {
		return errors.New("the checked backup went missing before it could be put in place")
	}
	m := st.Manifest
	markRestored(*data, m.Created, m.Version)
	fmt.Printf("Restored %s (%s, made %s by %s %s) into %s.\n", strings.Join(m.Files, ", "), map[bool]string{true: "with keys", false: "without keys"}[m.Keys],
		m.Created.Local().Format("2006-01-02 15:04"), "unagikuma", m.Version, *data)
	if before != "" {
		fmt.Printf("The files it replaced are kept in %s.\n", before)
	}
	if !m.Keys {
		fmt.Println("Put unagikuma.key back (or set UNAGIKUMA_SECRET_KEY) so stored secrets can be read; without the old cluster CA agents must be added again.")
	}
	return nil
}

// markRestored records that a restored copy did not crash: a backup taken
// from a running server carries its "still running" marker, which would
// otherwise be reported as an abnormal exit on the first start.
func markRestored(dir string, made time.Time, ver string) {
	st, err := store.Open(dir, store.Options{Tool: true})
	if err != nil {
		return
	}
	defer st.Close()
	_ = st.SetSetting("clean_shutdown", "1")
	_ = st.SetSetting("last_beat", "")
	cliAudit(st, "backup.restore", "", map[string]any{"made": made, "version": ver})
}
