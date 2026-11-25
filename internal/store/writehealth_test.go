package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// When the disk refuses writes, the batch stays in memory and is written
// once it accepts them again; meanwhile the store says it is failing.
func TestWriteFailuresAreKeptAndReported(t *testing.T) {
	s, c, _ := open(t)
	defer func(f func(*sql.DB) (*sql.Tx, error)) { beginBatch = f }(beginBatch)
	beginBatch = func(*sql.DB) (*sql.Tx, error) { return nil, errors.New("database or disk is full") }
	at := c.now()
	for i := range 5 {
		s.Verdict(verdict("web", at.Add(time.Duration(i)*time.Minute), "ok", true, 10))
	}
	s.Flush()
	wh := s.WriteHealth()
	if !wh.Failing || wh.Error != "database or disk is full" || wh.Waiting != 5 || wh.Lost != 0 {
		t.Fatalf("while failing: %+v", wh)
	}
	beginBatch = func(db *sql.DB) (*sql.Tx, error) { return db.Begin() }
	s.Flush()
	if wh := s.WriteHealth(); wh.Failing || wh.Waiting != 0 {
		t.Fatalf("after recovery: %+v", wh)
	}
	var n int
	if err := s.hist.QueryRow(`SELECT COUNT(*) FROM rounds`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("rounds written after the retry: %d %v", n, err)
	}
}
