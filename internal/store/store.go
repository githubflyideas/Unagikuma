// Package store persists unagikuma's runtime state in SQLite: core.db holds
// what must survive a crash exactly (incidents, notification intents,
// events, configuration history, users and secrets); history.db holds the
// per-round history and its aggregates, written in batches.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Store is the persistence layer. Safe for concurrent use.
type Store struct {
	dir   string
	core  *sql.DB
	hist  *sql.DB
	log   *slog.Logger
	now   func() time.Time
	wmu   sync.Mutex // serialises core writes
	batch *batcher
	tool  bool
	ids   monitorIDs

	agg5mSince time.Time
	ret        atomic.Pointer[Retention]
	auditFloor atomic.Int64 // --audit-retention, ns (0: the default)

	// Batch writer counters for /metrics.
	flushes, flushMicros, flushedItems atomic.Int64
	writes                             writeHealth

	// PrevClean reports whether the previous run shut down cleanly;
	// PrevBeat is its last liveness timestamp.
	PrevClean bool
	PrevBeat  time.Time
	FirstRun  bool
}

// Options configure Open.
type Options struct {
	Log *slog.Logger
	Now func() time.Time
	// CommitInterval batches history writes (storage.commit-interval).
	CommitInterval time.Duration
	// EvidenceKeep is how many evidence samples to keep per monitor
	// and node (probe.evidence-keep).
	EvidenceKeep int
	// Tool opens the store for a command-line tool that may run beside
	// the server: it leaves the server's shutdown and liveness markers
	// alone.
	Tool bool
}

func dsn(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_txlock=immediate"
}

// Open opens (creating or migrating) the databases in dir.
func Open(dir string, o Options) (*Store, error) {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CommitInterval <= 0 {
		o.CommitInterval = 2 * time.Second
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	s := &Store{dir: dir, log: o.Log, now: o.Now, tool: o.Tool}
	_, statErr := os.Stat(filepath.Join(dir, "core.db"))
	s.FirstRun = errors.Is(statErr, os.ErrNotExist)
	var err error
	if s.core, err = openDB(filepath.Join(dir, "core.db")); err != nil {
		return nil, openError(dir, err)
	}
	if s.hist, err = openDB(filepath.Join(dir, "history.db")); err != nil {
		s.core.Close()
		return nil, openError(dir, err)
	}
	if err := s.migrate(); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.chainAudit(); err != nil {
		s.Close()
		return nil, fmt.Errorf("audit log: %w", err)
	}
	if v, _ := s.Setting("never_started"); v == "1" {
		// Made by a tool (an import into a new directory): the first
		// server start is still the first run.
		s.FirstRun = true
		if !s.tool {
			_ = s.SetSetting("never_started", "")
		}
	}
	clean, _ := s.Setting("clean_shutdown")
	s.PrevClean = clean == "1" || s.FirstRun
	if v, _ := s.Setting("last_beat"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			s.PrevBeat = time.Unix(n, 0)
		}
	}
	if !s.tool {
		_ = s.SetSetting("clean_shutdown", "0")
	}
	s.batch = newBatcher(s, o.CommitInterval, o.EvidenceKeep)
	return s, nil
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	_ = os.Chmod(path, 0o600)
	return db, nil
}

// Close flushes pending writes, records a clean shutdown and closes.
func (s *Store) Close() error {
	if s.batch != nil {
		s.batch.stop()
	}
	if s.core != nil && !s.tool {
		_ = s.SetSetting("clean_shutdown", "1")
		_ = s.Beat()
	}
	var errs []error
	for _, db := range []*sql.DB{s.core, s.hist} {
		if db != nil {
			_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
			errs = append(errs, db.Close())
		}
	}
	return errors.Join(errs...)
}

// Beat records liveness (for the abnormal-exit notice of the next run).
func (s *Store) Beat() error {
	return s.SetSetting("last_beat", strconv.FormatInt(s.now().Unix(), 10))
}

// Setting reads a key from the settings table ("" when absent).
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.core.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting writes a settings key.
func (s *Store) SetSetting(key, value string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// DeleteSetting removes a settings key.
func (s *Store) DeleteSetting(key string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`DELETE FROM settings WHERE key=?`, key)
	return err
}

// Backup writes a consistent copy of core.db (VACUUM INTO) to w.
func (s *Store) Backup(ctx context.Context, w io.Writer) error { return s.BackupDB(ctx, "core", w) }

// BackupDB writes a consistent copy of "core" or "history" to w.
func (s *Store) BackupDB(ctx context.Context, which string, w io.Writer) error {
	db := s.core
	if which == "history" {
		s.batch.flushNow()
		db = s.hist
	}
	tmp := filepath.Join(s.dir, fmt.Sprintf(".backup-%s-%d.db", which, s.now().UnixNano()))
	defer os.Remove(tmp)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// Stats reports database sizes and row counts.
type Stats struct {
	CoreBytes    int64            `json:"core_bytes"`
	HistoryBytes int64            `json:"history_bytes"`
	Rows         map[string]int64 `json:"rows"`
}

// Stats computes storage statistics.
// DBBytes returns the on-disk size of core.db and history.db, each with
// its write-ahead log; cheap enough for every metrics scrape.
func (s *Store) DBBytes() (core, history int64) {
	size := func(name string) int64 {
		var n int64
		for _, suffix := range []string{"", "-wal"} {
			if fi, err := os.Stat(filepath.Join(s.dir, name+suffix)); err == nil {
				n += fi.Size()
			}
		}
		return n
	}
	return size("core.db"), size("history.db")
}

func (s *Store) Stats() Stats {
	st := Stats{Rows: map[string]int64{}}
	st.CoreBytes, st.HistoryBytes = s.DBBytes()
	for _, t := range []struct {
		db   *sql.DB
		name string
	}{{s.core, "incidents"}, {s.core, "events"}, {s.core, "deliveries"}, {s.core, "config_snapshots"}, {s.hist, "rounds"}, {s.hist, "agg5m"}, {s.hist, "hourly"}, {s.core, "evidence"}} {
		var n int64
		_ = t.db.QueryRow(`SELECT COUNT(*) FROM ` + t.name).Scan(&n)
		st.Rows[t.name] = n
	}
	return st
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// openError explains a database that cannot be opened. SQLite says "out
// of memory" or "unable to open database file" when the real cause is a
// data directory this user may not write to.
func openError(dir string, err error) error {
	f, werr := os.CreateTemp(dir, ".write-test-*")
	if werr == nil {
		name := f.Name()
		f.Close()
		os.Remove(name)
		return err
	}
	if errors.Is(werr, os.ErrPermission) || errors.Is(werr, syscall.EROFS) {
		return fmt.Errorf("the data directory %s is not writable for this user (%v): fix its owner or permissions, or pass another one with --data", dir, rootCause(werr))
	}
	return fmt.Errorf("cannot write in the data directory %s: %v", dir, rootCause(werr))
}

func rootCause(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
