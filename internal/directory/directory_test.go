package directory

import (
	"slices"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/store"
)

func TestResolve(t *testing.T) {
	d := New([]store.Person{
		{Handle: "li", Emails: []string{"li@example.com"}, Phones: []string{"+8613800000001"}},
		{Handle: "wang", Emails: []string{"wang@example.com"}, Phones: []string{"13800000002"}, LeftAt: time.Now()},
		{Handle: "noc", Emails: []string{"noc@example.com"}},
		{Handle: "zhao", Emails: []string{"zhao@example.com"}, Status: store.StatusLeave},
		{Handle: "sun", Emails: []string{"sun@example.com"}, Status: store.StatusTransferred},
	}, []store.BounceInfo{{Kind: store.ContactEmail, Address: "noc@example.com", Count: 3}, {Kind: store.ContactEmail, Address: "temp@example.com", Count: 1}}, 3)

	for _, c := range []struct {
		kind, list string
		send       []string
		codes      []string
	}{
		{store.ContactEmail, "li@example.com, Wang <WANG@example.com>", []string{"li@example.com"}, []string{IssueLeft}},
		{store.ContactEmail, "@li, @nobody, li@example.com", []string{"li@example.com"}, []string{IssueUnknownHandle}},
		// Only active people in the directory are sent to.
		{store.ContactEmail, "noc@example.com, temp@example.com", nil, []string{IssueNoRecipients, IssueBouncing, IssueUnknown}},
		{store.ContactEmail, "li@example.com, stranger@example.com", []string{"li@example.com"}, []string{IssueUnknown}},
		{store.ContactEmail, "@zhao, sun@example.com, li@example.com", []string{"li@example.com"}, []string{IssueInactive, IssueInactive}},
		{store.ContactPhone, "13800000001,+8613800000002", []string{"13800000001"}, []string{IssueLeft}},
		{store.ContactPhone, "@noc", nil, []string{IssueNoRecipients, IssueNoAddress}},
		{store.ContactPhone, "@wang", nil, []string{IssueNoRecipients, IssueLeft}},
	} {
		send, issues := d.Resolve(c.kind, c.list)
		var codes []string
		for _, i := range issues {
			codes = append(codes, i.Code)
		}
		if !slices.Equal(send, c.send) || !slices.Equal(codes, c.codes) {
			t.Errorf("%s %q: send %v issues %v, want %v %v", c.kind, c.list, send, codes, c.send, c.codes)
		}
	}
	if u := d.Unknown(store.ContactEmail, "li@example.com, new@example.com, @li"); !slices.Equal(u, []string{"new@example.com"}) {
		t.Errorf("unknown: %v", u)
	}
}

func TestSamePhone(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"+86 138-0000-0001", "13800000001", true},
		{"008613800000001", "+8613800000001", true},
		{"13800000001", "13800000002", false},
		{"+12025550100", "2025550100", true},
		{"+4412345", "12345", false}, // too short to be a national number
	} {
		if store.SamePhone(c.a, c.b) != c.same {
			t.Errorf("%s vs %s", c.a, c.b)
		}
	}
}
