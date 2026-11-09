package store

import (
	"errors"
	"time"
)

// ErrJoinCode means a join code is unknown, expired or already used.
var ErrJoinCode = errors.New("join code is invalid, expired or already used")

// CreateJoinCode issues a one-time code that lets a machine join the
// cluster within ttl. Only its hash is stored.
func (s *Store) CreateJoinCode(ttl time.Duration) (string, time.Time, error) {
	plain := "nkj_" + randomString(24)
	now := s.now()
	exp := now.Add(ttl)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO join_codes(hash,created_at,expires_at) VALUES(?,?,?)`, sha(plain), ms(now), ms(exp))
	if err != nil {
		return "", time.Time{}, err
	}
	// Forget codes that can no longer be used.
	_, _ = s.core.Exec(`DELETE FROM join_codes WHERE expires_at < ?`, ms(now.Add(-24*time.Hour)))
	return plain, exp, nil
}

// UseJoinCode consumes a code for node; it works once, before it expires.
func (s *Store) UseJoinCode(plain, node string) error {
	now := s.now()
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.core.Exec(`UPDATE join_codes SET used_at=?, node=? WHERE hash=? AND used_at IS NULL AND expires_at > ?`,
		ms(now), node, sha(plain), ms(now))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrJoinCode
	}
	return nil
}

// ReleaseJoinCode makes a consumed code usable again (the join failed
// after the code was taken).
func (s *Store) ReleaseJoinCode(plain string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.core.Exec(`UPDATE join_codes SET used_at=NULL, node=NULL WHERE hash=?`, sha(plain))
}
