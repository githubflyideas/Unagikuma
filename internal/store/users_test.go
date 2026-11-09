package store

import (
	"errors"
	"testing"
)

func TestUsersAndRoles(t *testing.T) {
	s, _, _ := open(t)
	admin, _ := s.CreateUser("admin", "secret123")
	op, err := s.CreateUserRole("bj-ops", "secret123", RoleOperator, []string{"region:north", "bj"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUserRole("bj-ops", "secret123", RoleViewer, nil); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.CreateUserRole("x", "secret123", "root", nil); err == nil {
		t.Fatal("unknown role accepted")
	}
	u, err := s.Authenticate("bj-ops", "secret123")
	if err != nil || u.Role != RoleOperator || len(u.Areas) != 2 || u.Areas[0] != "region:north" {
		t.Fatalf("login: %v %+v", err, u)
	}
	sid, _, _ := s.CreateSession(op, 3600e9)
	if se, _ := s.SessionByID(sid); se.User.Role != RoleOperator || len(se.User.Areas) != 2 {
		t.Fatalf("session: %+v", se.User)
	}
	// A role change ends the user's sessions.
	if err := s.SetRole(op, RoleViewer, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByID(sid); err == nil {
		t.Fatal("session survived a role change")
	}
	if err := s.SetRole(admin, RoleViewer, nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the last admin: %v", err)
	}
	if err := s.DeleteUser(admin); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the last admin: %v", err)
	}
	s.CreateToken(op, "ci", []string{"read"})
	if err := s.DeleteUser(op); err != nil {
		t.Fatal(err)
	}
	if toks, _ := s.Tokens(); len(toks) != 0 {
		t.Fatal("tokens of a deleted user must go")
	}
	list, _ := s.Users()
	if len(list) != 1 || list[0].Role != RoleAdmin || list[0].Areas == nil {
		t.Fatalf("users: %+v", list)
	}
}
