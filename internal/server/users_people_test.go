package server

import "testing"

// Every login is a person in the list: created with it, kept (without the
// login) when it is deleted, and not deletable while the login exists.
func TestLoginsArePeople(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	type person struct {
		ID       int64  `json:"id"`
		Handle   string `json:"handle"`
		Username string `json:"username"`
	}
	list := func() []person {
		var r struct {
			People []person `json:"people"`
		}
		e.do("GET", "/api/v1/people", nil, &r)
		return r.People
	}
	find := func(handle string) *person {
		for _, p := range list() {
			if p.Handle == handle {
				return &p
			}
		}
		return nil
	}
	// The administrator made at setup is in the list.
	if p := find("admin"); p == nil || p.Username != "admin" {
		t.Fatalf("admin: %+v", list())
	}
	var made struct{ ID int64 }
	if c := e.do("POST", "/api/v1/users", map[string]any{"username": "Li.Lei", "password": "secret-123", "role": "viewer"}, &made); c != 200 {
		t.Fatalf("create user: %d", c)
	}
	p := find("li.lei")
	if p == nil || p.Username != "Li.Lei" {
		t.Fatalf("new login not listed: %+v", list())
	}
	if c := e.do("DELETE", "/api/v1/people/"+itoa(p.ID), nil, nil); c != 409 {
		t.Fatalf("person of a login deleted: %d", c)
	}
	if c := e.do("DELETE", "/api/v1/users/"+itoa(made.ID), nil, nil); c != 200 {
		t.Fatalf("delete user: %d", c)
	}
	if p := find("li.lei"); p == nil || p.Username != "" {
		t.Fatalf("after the login went: %+v", p)
	}
	// A login for someone already in the list goes to them, not to a new
	// entry.
	var pr struct{ ID int64 }
	if c := e.do("POST", "/api/v1/people", map[string]any{"handle": "wang", "name": "Wang Wu", "emails": []string{"wang@example.com"}}, &pr); c != 200 {
		t.Fatalf("person: %d", c)
	}
	n := len(list())
	if c := e.do("POST", "/api/v1/users", map[string]any{"username": "wangwu", "password": "secret-123", "role": "viewer", "person": pr.ID}, nil); c != 200 {
		t.Fatalf("login for a person: %d", c)
	}
	if p := find("wang"); p == nil || p.Username != "wangwu" || len(list()) != n {
		t.Fatalf("linked: %+v (%d people, was %d)", p, len(list()), n)
	}
}
