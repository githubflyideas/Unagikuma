package engine

import (
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/notify"
)

// The end of a system problem says so in its title: a node that came back
// is not announced as "offline" again.
func TestSystemRecoveryTitle(t *testing.T) {
	e := New(Deps{})
	for _, c := range []struct{ key, title, want string }{
		{SystemNodeOffline + ":bj", "Node bj is offline", "Node bj is back online"},
		{SystemAllFailed, "All notification channels failed", "Notification channels work again"},
		{SystemLocalNetwork, "Local network outage", "This host's network is back"},
	} {
		inc := &Incident{Kind: IncidentSystem, System: c.key, Title: c.title, Summary: "details"}
		msg := e.message(inc, PlannedNotice{Kind: NoticeRecovery}, t0)
		if msg.Title != c.want || msg.Status != StatusOK || msg.Body != "details" {
			t.Errorf("%s: %+v", c.key, msg)
		}
		if title, _ := notify.Render(&msg); !strings.HasPrefix(title, "🟢") || !strings.Contains(title, c.want) {
			t.Errorf("%s rendered as %q", c.key, title)
		}
		if msg := e.message(inc, PlannedNotice{Kind: NoticeProblem}, t0); msg.Title != c.title {
			t.Errorf("%s problem title %q", c.key, msg.Title)
		}
	}
}
