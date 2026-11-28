package app

import (
	"errors"
	"fmt"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/license"
)

// Without a licence an installation may have license.FreeUsers accounts
// and a cluster license.FreeNodes nodes, the master included (a single
// installation is a cluster of one, so it counts 1); a licence states its
// own numbers (0 = no limit) and never gives fewer. A licence of an older
// format carries no node limit and therefore allows any number of nodes.
// Nothing is switched off when a number is exceeded, for example after a
// licence is removed or expires; only adding more is refused.

// ErrQuota means adding one more would exceed the licence.
var ErrQuota = errors.New("licence quota")

// QuotaError says which quota and its limit.
type QuotaError struct {
	Kind  string // nodes | users
	Used  int
	Limit int
}

func (e *QuotaError) Error() string {
	if e.Kind == "nodes" {
		return fmt.Sprintf("a cluster may have %d nodes, the master included, and %d are in use; register for more (Settings › Registration)", e.Limit, e.Used)
	}
	return fmt.Sprintf("an installation may have %d %s and %d are in use; register for more (Settings › Registration)", e.Limit, e.Kind, e.Used)
}

func (e *QuotaError) Unwrap() error { return ErrQuota }

// Usage is what counts against the quotas.
type Usage struct {
	Nodes    int `json:"nodes"` // the master and every probing node
	Users    int `json:"users"`
	Monitors int `json:"monitors"` // the master's running set (see monitorcap.go)
}

// Usage counts nodes, accounts and monitors.
func (a *App) Usage() Usage {
	u := Usage{Nodes: CountNodes(a.Config().Config)}
	u.Monitors = a.MonitorCap().Used // a node: the cluster's, from the master
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
	switch kind {
	case "users":
		if limit := UserLimit(st); limit > 0 && u.Users+1 > limit {
			return &QuotaError{Kind: "accounts", Used: u.Users, Limit: limit}
		}
	case "nodes":
		if limit := NodeLimit(st); limit > 0 && u.Nodes+1 > limit {
			return &QuotaError{Kind: "nodes", Used: u.Nodes, Limit: limit}
		}
	}
	return nil
}

// CountNodes counts the nodes of cfg: the master and every other NODE
// record. A configuration without nodes is one machine, counting 1.
func CountNodes(cfg *config.Config) int {
	if cfg == nil {
		return 1
	}
	n := 1
	for _, nd := range cfg.Nodes {
		if nd.Name != cfg.MName {
			n++
		}
	}
	return n
}

// NodeLimit is the number of nodes the installation may have, the master
// included (0 = no limit). A licence never gives fewer than the free
// number; one without a node limit (an older format, or none stated)
// gives no limit.
func NodeLimit(st license.State) int {
	if !st.Registered {
		return license.FreeNodes
	}
	if st.MaxNodes > 0 && st.MaxNodes < license.FreeNodes {
		return license.FreeNodes
	}
	return st.MaxNodes
}

// NodeCap is the installation's standing against the node limit, for the
// over-limit notice.
type NodeCap struct {
	Used       int  `json:"used"`
	Limit      int  `json:"limit"` // 0 = no limit
	Over       bool `json:"over"`
	Registered bool `json:"registered"`
}

// NodeCap reports the current standing against the node limit.
func (a *App) NodeCap() NodeCap {
	st := a.License().State()
	limit, used := NodeLimit(st), CountNodes(a.Config().Config)
	return NodeCap{Used: used, Limit: limit, Over: limit > 0 && used > limit, Registered: st.Registered}
}

// nodeCapDiag is the refusal a configuration change gets when it would
// raise the number of nodes past the limit and past what is already
// there: only growth beyond the limit is refused, never the status quo,
// and never a change that holds or lowers the count (the same rule as
// monitorCapDiag).
func (a *App) nodeCapDiag(old, new *config.Config) *config.Diagnostic {
	limit := NodeLimit(a.License().State())
	if limit == 0 {
		return nil
	}
	nn := CountNodes(new)
	if nn <= limit || nn <= CountNodes(old) {
		return nil
	}
	return &config.Diagnostic{Pos: config.Pos{Line: 1, Col: 1}, Severity: config.SevError, Code: "node_limit",
		Msg: fmt.Sprintf("this change would raise the number of nodes to %d, past this installation's limit of %d (the master counts); to register, please contact the author (Settings › Registration)", nn, limit)}
}

// capDiag is the licence ceiling a configuration change meets: the
// monitor limit (unless skipMonitors, for a change the monitor limit does
// not govern) and the node limit.
func (a *App) capDiag(old, new *config.Config, skipMonitors bool) *config.Diagnostic {
	if !skipMonitors {
		if d := a.monitorCapDiag(old, new); d != nil {
			return d
		}
	}
	return a.nodeCapDiag(old, new)
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
