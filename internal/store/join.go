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
	return s.createJoinCode(ttl, "")
}

// CreateRejoinCode issues a one-time code that may only reclaim the named
// existing NODE (replaced master or healed split-brain peer).
func (s *Store) CreateRejoinCode(ttl time.Duration, node string) (string, time.Time, error) {
	if node == "" {
		return "", time.Time{}, errors.New("rejoin node required")
	}
	return s.createJoinCode(ttl, node)
}

func (s *Store) createJoinCode(ttl time.Duration, rejoin string) (string, time.Time, error) {
	plain := "nkj_" + randomString(24)
	now := s.now()
	exp := now.Add(ttl)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO join_codes(hash,created_at,expires_at,rejoin) VALUES(?,?,?,?)`, sha(plain), ms(now), ms(exp), rejoin)
	if err != nil {
		return "", time.Time{}, err
	}
	// Forget codes that can no longer be used.
	_, _ = s.core.Exec(`DELETE FROM join_codes WHERE expires_at < ?`, ms(now.Add(-24*time.Hour)))
	return plain, exp, nil
}

// UseJoinCode consumes a code for node; it works once, before it expires.
// A rejoin code may only be used for its intended node name.
func (s *Store) UseJoinCode(plain, node string) error {
	now := s.now()
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var rejoin string
	err := s.core.QueryRow(`SELECT rejoin FROM join_codes WHERE hash=? AND used_at IS NULL AND expires_at > ?`, sha(plain), ms(now)).Scan(&rejoin)
	if err != nil {
		return ErrJoinCode
	}
	if rejoin != "" && rejoin != node {
		return ErrJoinCode
	}
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

// JoinCodeRejoin reports the NODE a code is reserved to reclaim, if any.
func (s *Store) JoinCodeRejoin(plain string) string {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var rejoin string
	_ = s.core.QueryRow(`SELECT rejoin FROM join_codes WHERE hash=?`, sha(plain)).Scan(&rejoin)
	return rejoin
}

// ReleaseJoinCode makes a consumed code usable again (the join failed
// after the code was taken).
func (s *Store) ReleaseJoinCode(plain string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.core.Exec(`UPDATE join_codes SET used_at=NULL, node=NULL WHERE hash=?`, sha(plain))
}
