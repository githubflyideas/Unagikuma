package engine

import (
	"testing"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

const (
	ok   = probe.VoteOK
	fail = probe.VoteFail
	abst = probe.VoteAbstain
)

func nv(node string, vote probe.Vote) NodeVote {
	return NodeVote{Node: node, Online: true, Present: true, Vote: vote}
}

func offline(node string) NodeVote { return NodeVote{Node: node} }

func localAbstain(node, reason string) NodeVote {
	return NodeVote{Node: node, Online: true, Present: true, Vote: abst, Reason: reason}
}

// Quorum parsing remains for importing old files, not runtime judgement.
func TestLocalVerdict(t *testing.T) {
	for _, tc := range []struct {
		vote   NodeVote
		kind   VerdictKind
		reason string
	}{
		{nv("a", ok), VerdictOK, ""}, {nv("a", fail), VerdictCritical, ""},
		{localAbstain("a", probe.ReasonPrivilege), VerdictUnknown, UnknownAllAbstain},
		{NodeVote{Node: "a", Online: true, Present: true, Excluded: true, Vote: fail}, VerdictUnknown, UnknownLocalNetwork},
		{offline("a"), VerdictUnknown, UnknownAllAbstain},
	} {
		v := LocalVerdict(tc.vote)
		if v.Kind != tc.kind || v.UnknownReason != tc.reason || v.Summary() != "" {
			t.Fatalf("%+v -> %+v", tc, v)
		}
	}
}
