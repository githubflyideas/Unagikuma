package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/githubflyideas/unagikuma/internal/config"
	"os"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/backup"
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
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	m, err := backup.Restore(f, *data, *cfgPath, pass, *force)
	if err != nil {
		return err
	}
	markRestored(*data, m.Created, m.Version)
	fmt.Printf("Restored %s (%s, made %s by %s %s) into %s.\n", strings.Join(m.Files, ", "), map[bool]string{true: "with keys", false: "without keys"}[m.Keys],
		m.Created.Local().Format("2006-01-02 15:04"), "unagikuma", m.Version, *data)
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
