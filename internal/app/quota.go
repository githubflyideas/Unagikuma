package app

import (
	"errors"
	"fmt"

	"github.com/githubflyideas/unagikuma/internal/license"
)

// Without a licence an installation may have license.FreeUsers accounts;
// a licence states its own number (0 = no limit) and never gives fewer.
// Nodes are not limited. Nothing is switched off when the number is
// exceeded, for example after a licence is removed; only adding more is
// refused.

// ErrQuota means adding one more would exceed the licence.
var ErrQuota = errors.New("licence quota")

// QuotaError says which quota and its limit.
type QuotaError struct {
	Kind  string // nodes | users
	Used  int
	Limit int
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("an installation may have %d %s and %d are in use; register for more (Settings › Registration)", e.Limit, e.Kind, e.Used)
}

func (e *QuotaError) Unwrap() error { return ErrQuota }

// Usage is what counts against the quotas.
type Usage struct {
	Nodes int `json:"nodes"` // the master and every probing node
	Users int `json:"users"`
}

// Usage counts nodes and accounts.
func (a *App) Usage() Usage {
	u := Usage{Nodes: 1}
	cfg := a.Config().Config
	for _, n := range cfg.Nodes {
		if n.Name != cfg.MName {
			u.Nodes++
		}
	}
	if users, err := a.store.Users(); err == nil {
		u.Users = len(users)
	}
	return u
}

// AllowAdd checks that one more node or user stays within the licence.
func (a *App) AllowAdd(kind string) error {
	st := a.License().State()
	return allowAdd(st, a.Usage(), kind)
}

func allowAdd(st license.State, u Usage, kind string) error {
	if kind != "users" {
		return nil
	}
	if limit := UserLimit(st); limit > 0 && u.Users+1 > limit {
		return &QuotaError{Kind: "accounts", Used: u.Users, Limit: limit}
	}
	return nil
}

// UserLimit is the number of accounts the installation may have (0 = no
// limit).
func UserLimit(st license.State) int {
	if !st.Registered {
		return license.FreeUsers
	}
	if st.MaxUsers > 0 && st.MaxUsers < license.FreeUsers {
		return license.FreeUsers
	}
	return st.MaxUsers
}
