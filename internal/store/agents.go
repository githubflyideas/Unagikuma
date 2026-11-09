package store

import (
	"database/sql"
	"errors"
	"time"
)

// AgentToken describes the credential of a probe node. Only its hash is
// stored; the plaintext is shown once when it is created.
type AgentToken struct {
	Node      string     `json:"node"`
	Prefix    string     `json:"prefix"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
	LastAddr  string     `json:"last_addr,omitempty"`
}

// CreateAgentToken issues a new token for node, revoking any previous one.
func (s *Store) CreateAgentToken(node string) (string, error) {
	plain := "uka_" + randomString(30)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`INSERT INTO agent_tokens(node,prefix,hash,created_at) VALUES(?,?,?,?)
		ON CONFLICT(node) DO UPDATE SET prefix=excluded.prefix, hash=excluded.hash, created_at=excluded.created_at, last_used=NULL, last_addr=NULL`,
		node, plain[:12], sha(plain), ms(s.now()))
	if err != nil {
		return "", err
	}
	return plain, nil
}

// AgentNodeByToken resolves an agent token to its node and records the use.
func (s *Store) AgentNodeByToken(plain, addr string) (string, error) {
	var node string
	err := s.core.QueryRow(`SELECT node FROM agent_tokens WHERE hash=?`, sha(plain)).Scan(&node)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.core.Exec(`UPDATE agent_tokens SET last_used=?, last_addr=? WHERE node=?`, ms(s.now()), addr, node)
	return node, err
}

// AgentTokens lists the issued agent tokens by node name.
func (s *Store) AgentTokens() ([]AgentToken, error) {
	rows, err := s.core.Query(`SELECT node,prefix,created_at,last_used,last_addr FROM agent_tokens ORDER BY node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentToken{}
	for rows.Next() {
		var t AgentToken
		var created int64
		var last sql.NullInt64
		var addr sql.NullString
		if err := rows.Scan(&t.Node, &t.Prefix, &created, &last, &addr); err != nil {
			return nil, err
		}
		t.CreatedAt, t.LastAddr = fromMS(created), addr.String
		if last.Valid {
			lu := fromMS(last.Int64)
			t.LastUsed = &lu
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAgentToken revokes a node's token.
func (s *Store) DeleteAgentToken(node string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`DELETE FROM agent_tokens WHERE node=?`, node)
	return err
}

// RenameAgentToken moves a node's token to a new name.
func (s *Store) RenameAgentToken(oldName, newName string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.core.Exec(`UPDATE agent_tokens SET node=? WHERE node=?`, newName, oldName)
	return err
}
