package postmortem_test

import (
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/postmortem"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// One configuration change touches a monitor of this site and a monitor
// of another team. A member of this site's team must see its own record
// and nothing about the other record: not its name, not its values.
func TestOwnChangesLeaveOutOtherRecordsOfTheSameChange(t *testing.T) {
	now := t0.Add(-20 * time.Hour)
	st, err := store.Open(t.TempDir(), store.Options{Now: func() time.Time { return now }, CommitInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.Audit(store.AuditEntry{Actor: "alice", Action: "config.apply", Target: "monitors.conf",
		Detail: map[string]any{
			"diff": []map[string]string{
				{"record": "web", "type": "monitor", "kind": "changed", "before": "a", "after": "b"},
				{"record": "payments-db", "type": "monitor", "kind": "changed", "before": "PAYSECRET-OLD", "after": "PAYSECRET-NEW"},
			},
			"monitors": []string{"web", "payments-db"},
		}})
	now = t0.Add(10 * time.Minute)
	for at := t0.Add(-time.Hour); at.Before(t0.Add(5 * time.Minute)); at = at.Add(30 * time.Second) {
		v, s, hard := engine.VerdictOK, "ok", true
		if !at.Before(t0) {
			v, s, hard = engine.VerdictCritical, "critical", !at.Before(t0.Add(time.Minute))
		}
		st.Verdict(engine.VerdictRecord{Monitor: "web", At: at, Verdict: v, Status: s, Hard: hard, Reason: "http_status", Interval: 30 * time.Second})
	}
	inc := &engine.Incident{Monitor: "web", Kind: "normal", Policy: "default", Reason: "http_status", Detail: "HTTP 500", Title: "Web",
		State: engine.IncidentState{OpenedAt: t0.Add(time.Minute), HardAt: t0.Add(time.Minute), ResolvedAt: t0.Add(4 * time.Minute)}}
	if err := st.SaveIncident(inc); err != nil {
		t.Fatal(err)
	}
	cov := postmortem.Coverage{Known: true, Type: "HTTP", Target: "https://web.example/", Depth: "L3", Interval: 30 * time.Second, Attempts: 2, Timeout: 10 * time.Second}

	rep, err := postmortem.Build(st, inc.ID, postmortem.Options{Lang: "en", Location: time.UTC, Now: now, Coverage: cov, OwnChangesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 1 {
		t.Fatalf("own changes: want the one entry, got %d", len(rep.Changes))
	}
	md := rep.Markdown()
	for _, leak := range []string{"payments-db", "PAYSECRET"} {
		if strings.Contains(md, leak) {
			t.Errorf("report leaks %q from another team's record:\n%s", leak, md)
		}
	}
	if !strings.Contains(md, "web") {
		t.Errorf("report lost this monitor's own change:\n%s", md)
	}
	if d, _ := rep.Changes[0].Detail["diff"].([]any); len(d) != 1 {
		t.Errorf("diff of the kept entry should hold only web, got %v", rep.Changes[0].Detail["diff"])
	}
	if rep.Changes[0].Target != "web" {
		t.Errorf("the kept change is labelled %q, not with the monitor", rep.Changes[0].Target)
	}
	if _, ok := rep.Changes[0].Detail["monitors"]; ok {
		t.Errorf("other keys of the entry leak the full list of changed monitors")
	}
}
