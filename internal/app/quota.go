package app

import (
	"errors"
	"fmt"

	"github.com/githubflyideas/unagikuma/internal/license"
)

// Quotas come from the licence: a registered installation may add nodes
// and accounts up to what it states (0 = no limit). Nothing is switched
// off when they are exceeded, for example after a smaller licence is
// installed; only adding more is refused. Without a licence there is no
// limit.

// ErrQuota means adding one more would exceed the licence.
var ErrQuota = errors.New("licence quota")

// QuotaError says which quota and its limit.
type QuotaError struct {
	Kind  string // nodes | users
	Used  int
	Limit int
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("the licence allows %d %s and %d are in use; install a licence with a higher limit (Settings › Registration)", e.Limit, e.Kind, e.Used)
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
	if !st.Registered {
		return nil
	}
	used, limit := u.Users, st.MaxUsers
	if kind == "nodes" {
		used, limit = u.Nodes, st.MaxNodes
	}
	if limit > 0 && used+1 > limit {
		return &QuotaError{Kind: kind, Used: used, Limit: limit}
	}
	return nil
}
