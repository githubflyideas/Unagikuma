// Package engine is unagikuma's decision core: local checks, the
// SOFT/HARD state machine, incidents and notification discipline, local
// network anomaly and suspend detection.
//
// The files verdict.go, state.go, localnet.go, suspend.go and plan.go are pure
// logic (no I/O, explicit time) so the decision tables can be tested exhaustively.
package engine

import (
	"fmt"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// VerdictKind is the outcome of one round.
type VerdictKind string

const (
	VerdictOK       VerdictKind = "ok"
	VerdictPartial  VerdictKind = "partial"
	VerdictCritical VerdictKind = "critical"
	VerdictUnknown  VerdictKind = "unknown"
)

// UNKNOWN reasons, in priority order.
const (
	UnknownLocalNetwork   = "local_network"      // healthy nodes = 0 because of local network anomaly / isolation
	UnknownAllAbstain     = "all_abstain"        // nodes online but all abstained for local reasons
	UnknownNoHealthyNodes = "no_healthy_nodes"   // all participants offline (multi-node only)
	UnknownInsufficient   = "insufficient_votes" // healthy nodes exist but not enough valid votes
)

// NodeVote is one participant's input to a round.
type NodeVote struct {
	Node string
	// Online is false when the node was not connected when the round was
	// dispatched (always true for the local node).
	Online bool
	// Excluded is true when the node is excluded for this monitor's scope and
	// address family by local network anomaly detection or isolation
	//.
	Excluded bool
	// Present is false when no vote arrived before the round deadline.
	Present bool
	Vote    probe.Vote
	Reason  string
}

// QuorumRule is retained only for legacy config/API compatibility; it is not a runtime gate.
type QuorumRule struct {
	Spec           string // "majority" (default), "all", "N", "N%"
	OnNodeLoss     string // "degrade" (default) | "unknown"
	OnInsufficient string // "unknown" (default) | "critical"
	// Weak is true for L1 and stateful templates.
	Weak bool
}

// Verdict of one round.
type Verdict struct {
	Kind          VerdictKind
	Fail, OK      int
	N, Healthy    int
	Q, QEff       int
	Degraded      bool // quorum recomputed because of node loss
	UnknownReason string
	FailingNodes  []string
	OKNodes       []string
}

// Summary is the quorum text used in notifications; empty for one node so
// single-machine messages carry no quorum line.
func (v Verdict) Summary() string {
	if v.N <= 1 {
		return ""
	}
	s := fmt.Sprintf("%d/%d failed (need %d)", v.Fail, v.N, v.QEff)
	if len(v.FailingNodes) > 0 {
		s += ": " + strings.Join(v.FailingNodes, ",")
	}
	return s
}

// LocalVerdict is the only runtime target verdict. Remote observations never vote.
func LocalVerdict(nv NodeVote) Verdict {
	v := Verdict{N: 1, Q: 1, QEff: 1, Kind: VerdictUnknown, UnknownReason: UnknownAllAbstain}
	if nv.Excluded {
		v.UnknownReason = UnknownLocalNetwork
		return v
	}
	if !nv.Present || !nv.Online {
		return v
	}
	switch nv.Vote {
	case probe.VoteOK:
		v.Kind = VerdictOK
		v.OK = 1
		v.Healthy = 1
		v.OKNodes = []string{nv.Node}
	case probe.VoteFail:
		v.Kind = VerdictCritical
		v.Fail = 1
		v.Healthy = 1
		v.FailingNodes = []string{nv.Node}
	}
	if v.Kind != VerdictUnknown {
		v.UnknownReason = ""
	}
	return v
}
