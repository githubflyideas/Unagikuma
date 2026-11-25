package store

import (
	"errors"
	"testing"
)

func TestAgentTokens(t *testing.T) {
	s, _, _ := open(t)
	tok, err := s.CreateAgentToken("bj")
	if err != nil || len(tok) < 20 {
		t.Fatalf("create: %q %v", tok, err)
	}
	if n, err := s.AgentNodeByToken(tok, "10.0.0.9:5555"); n != "bj" || err != nil {
		t.Fatalf("resolve: %q %v", n, err)
	}
	if _, err := s.AgentNodeByToken("uka_wrong", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: %v", err)
	}
	list, _ := s.AgentTokens()
	if len(list) != 1 || list[0].Node != "bj" || list[0].LastUsed == nil || list[0].LastAddr != "10.0.0.9:5555" || list[0].Prefix != tok[:12] {
		t.Fatalf("list: %+v", list)
	}

	// Rotating revokes the old token.
	tok2, _ := s.CreateAgentToken("bj")
	if _, err := s.AgentNodeByToken(tok, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("old token still valid after rotation")
	}
	if err := s.RenameAgentToken("bj", "beijing"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.AgentNodeByToken(tok2, ""); n != "beijing" {
		t.Fatalf("renamed: %q", n)
	}
	s.DeleteAgentToken("beijing")
	if _, err := s.AgentNodeByToken(tok2, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted token still valid")
	}
}
