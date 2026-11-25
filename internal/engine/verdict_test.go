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

func TestQuorumOf(t *testing.T) {
	cases := []struct {
		spec string
		n, q int
	}{
		{"majority", 1, 1}, {"majority", 2, 2}, {"majority", 3, 2}, {"majority", 4, 3}, {"majority", 5, 3},
		{"all", 3, 3}, {"1", 3, 1}, {"9", 3, 3}, {"60%", 5, 3}, {"50%", 4, 2}, {"34%", 3, 2}, {"", 3, 2},
	}
	for _, c := range cases {
		if q, err := QuorumOf(c.spec, c.n); err != nil || q != c.q {
			t.Errorf("QuorumOf(%q,%d)=%d,%v want %d", c.spec, c.n, q, err, c.q)
		}
	}
	if _, err := QuorumOf("abc", 3); err == nil {
		t.Error("bad spec accepted")
	}
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
