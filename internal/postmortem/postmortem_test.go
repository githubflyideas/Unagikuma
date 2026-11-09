package postmortem_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/postmortem"
	"github.com/githubflyideas/unagikuma/internal/store"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// A recorded incident gives a report with the four parts in order, the
// change made the evening before, the alert and its receipt, the gap in
// monitoring, what the check covers and a plain conclusion.
func TestReportTellsTheStory(t *testing.T) {
	now := t0.Add(-20 * time.Hour)
	st, err := store.Open(t.TempDir(), store.Options{Now: func() time.Time { return now }, CommitInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// The evening before: someone changes the monitor.
	_ = st.Audit(store.AuditEntry{Actor: "alice", Action: "config.apply", Target: "monitors.conf",
		Detail: map[string]any{"diff": []map[string]string{{"record": "web", "type": "monitor", "kind": "changed", "before": "a", "after": "b"}}}})
	now = t0.Add(10 * time.Minute)
	// Rounds every 30s from 11:00: fine until 12:00, then failing; a
	// gap of 5 minutes at 11:30.
	for at := t0.Add(-time.Hour); at.Before(t0.Add(5 * time.Minute)); at = at.Add(30 * time.Second) {
		if at.After(t0.Add(-30*time.Minute)) && at.Before(t0.Add(-25*time.Minute)) {
			continue
		}
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
	st.Decision(engine.Decision{At: t0.Add(time.Minute), IncidentID: inc.ID, IntentID: "i1", Monitor: "web", Kind: "PROBLEM", Step: engine.LedQueued,
		Class: engine.ClassBusiness, Evidence: map[string]any{"reason": "http_status"}, Recipients: []string{"ops"}})
	st.Decision(engine.Decision{At: t0.Add(time.Minute + 2*time.Second), IncidentID: inc.ID, IntentID: "i1", Monitor: "web", Kind: "PROBLEM", Step: engine.LedSent,
		Recipients: []string{"ops"}, Receipts: []notify.Attempt{{Channel: "ops", OK: true, Status: 200}}})
	st.Delivery(engine.DeliveryRecord{IntentID: "i1", IncidentID: inc.ID, Kind: "PROBLEM", Seq: 1, At: t0.Add(time.Minute + 2*time.Second),
		Attempts: []notify.Attempt{{Channel: "ops", OK: true}}, OK: true})
	st.Delivery(engine.DeliveryRecord{IntentID: "i2", IncidentID: inc.ID, Kind: "RECOVERY", Seq: 1, At: t0.Add(4 * time.Minute),
		Attempts: []notify.Attempt{{Channel: "ops", OK: true}}, OK: true})

	cov := postmortem.Coverage{Known: true, Type: "HTTP", Target: "https://web.example/", Depth: "L3", Interval: 30 * time.Second, Attempts: 2, Timeout: 10 * time.Second}
	rep, err := postmortem.Build(st, inc.ID, postmortem.Options{Lang: "en", Location: time.UTC, Now: now, Coverage: cov})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.FailedAt.Equal(t0) || !rep.NotifiedAt.Equal(t0.Add(time.Minute+2*time.Second)) || rep.Class != engine.ClassBusiness {
		t.Fatalf("times: failed %v notified %v class %s", rep.FailedAt, rep.NotifiedAt, rep.Class)
	}
	keys := []string{}
	for _, s := range rep.Segments {
		keys = append(keys, s.Key)
	}
	if strings.Join(keys, ",") != "lead_up,detection,notification,recovery" {
		t.Fatalf("segments %v", keys)
	}
	if len(rep.Liveness.Gaps) != 1 || rep.Liveness.Watched {
		t.Errorf("the 5-minute gap: %+v", rep.Liveness)
	}
	md := rep.Markdown()
	for _, want := range []string{
		"# Incident #1 report: web",
		"alice changed config.apply monitors.conf — this changed the monitor itself",
		"first failed check:", "failure confirmed after 3 checks",
		"PROBLEM alert delivered to ops: ops ✓ (200)",
		"first alert delivered 2s after the failure was confirmed, to ops",
		"recovered after 4m",
		"no checks from",
		"Depth L3", "Checked from this server only",
		"web was down from 2026-10-01 12:00:00 UTC to 2026-10-01 12:04:00 UTC (4m).",
		"Kind of failure: service failure.",
		"The monitor's own configuration was changed in the 24 hours before (changes: 1)",
		"Monitoring was interrupted (gaps: 1,",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Index(md, "## 1.") > strings.Index(md, "## 2.") || strings.Index(md, "## 3.") > strings.Index(md, "## 4.") {
		t.Error("segments out of order")
	}
	var buf bytes.Buffer
	if err := rep.Write(&buf, "json"); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || back["conclusion"] == "" || back["segments"] == nil {
		t.Fatalf("json: %v %v", err, back["conclusion"])
	}
	buf.Reset()
	if err := rep.Write(&buf, "html"); err != nil || !strings.Contains(buf.String(), "<h2>1. ") || strings.Contains(buf.String(), "<script") {
		t.Fatalf("html: %v %s", err, buf.String())
	}
	if err := rep.Write(&buf, "pdf"); err == nil {
		t.Error("unknown format accepted")
	}
	// Every language has every line.
	for _, lang := range []string{"zh", "ja", "ar", "ru"} {
		r2, _ := postmortem.Build(st, inc.ID, postmortem.Options{Lang: lang, Location: time.UTC, Now: now, Coverage: cov})
		if m := r2.Markdown(); strings.Contains(m, "pm.") {
			t.Errorf("%s: untranslated key in\n%s", lang, m)
		}
	}
	if _, err := postmortem.Build(st, 999, postmortem.Options{}); err != postmortem.ErrNotFound {
		t.Errorf("unknown incident: %v", err)
	}
}
