package store

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestImportedBcryptUser(t *testing.T) {
	s, _, _ := open(t)
	h, _ := bcrypt.GenerateFromPassword([]byte("kuma-pass-1"), 4)
	if _, err := s.CreateUserHash("alice", string(h), RoleOperator, []string{"region:north"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUserHash("bob", "plain", RoleViewer, nil); err == nil {
		t.Fatal("a non-hash was accepted")
	}
	if _, err := s.Authenticate("alice", "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	u, err := s.Authenticate("alice", "kuma-pass-1")
	if err != nil || u.Role != RoleOperator || len(u.Areas) != 1 {
		t.Fatalf("login: %+v %v", u, err)
	}
	var hash string
	_ = s.core.QueryRow(`SELECT password FROM users WHERE username='alice'`).Scan(&hash)
	if isBcrypt(hash) || !CheckPassword(hash, "kuma-pass-1") {
		t.Fatalf("hash not upgraded: %s", hash)
	}
}

func TestKeyPersists(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "unagikuma.key")); err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(dir)
	if err != nil || string(k1) != string(k2) {
		t.Fatal("the key was not reused: stored secrets would be lost")
	}
}
