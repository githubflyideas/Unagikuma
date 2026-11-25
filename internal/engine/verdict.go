// Package engine is unagikuma's decision core: local checks, the
// SOFT/HARD state machine, incidents and notification discipline, local
// network anomaly and suspend detection.
//
// The files verdict.go, state.go, localnet.go, suspend.go and plan.go are pure
// logic (no I/O, explicit time) so the decision tables can be tested exhaustively.
package engine

import (
	"fmt"
	"math"
	"strconv"
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

type quorumKind int

const (
	qMajority quorumKind = iota
	qAll
	qPercent
	qCount
)

func parseQuorum(spec string) (quorumKind, float64, error) {
	s := strings.TrimSpace(strings.ToLower(spec))
	switch {
	case s == "" || s == "majority":
		return qMajority, 0, nil
	case s == "all":
		return qAll, 0, nil
	case strings.HasSuffix(s, "%"):
		p, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		// NaN fails every comparison, so test the good range, not the bad.
		if err != nil || !(p > 0 && p <= 100) {
			return 0, 0, fmt.Errorf("bad quorum %q", spec)
		}
		return qPercent, p, nil
	default:
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, 0, fmt.Errorf("bad quorum %q", spec)
		}
		return qCount, float64(n), nil
	}
}

func quorumFor(kind quorumKind, arg float64, n int) int {
	if n <= 0 {
		return 0
	}
	var q int
	switch kind {
	case qMajority:
		q = n/2 + 1
	case qAll:
		q = n
	case qPercent:
		q = int(math.Ceil(float64(n) * arg / 100)) // percentages round up
	case qCount:
		q = int(arg)
	}
	return max(1, min(q, n))
}

// QuorumOf resolves a quorum spec for n participants.
func QuorumOf(spec string, n int) (int, error) {
	k, arg, err := parseQuorum(spec)
	if err != nil {
		return 0, err
	}
	return quorumFor(k, arg, n), nil
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
