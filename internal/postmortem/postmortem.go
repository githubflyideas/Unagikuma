// Package postmortem writes the report of one incident: what led up to
// it, how it was detected, who was told and when, how it ended, whether
// monitoring itself was running the whole time, and what the check can
// and cannot see. It reads only what was recorded, so it can be produced
// long after the fact, from the server or the command line.
package postmortem

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// ErrNotFound is returned for an unknown incident.
var ErrNotFound = errors.New("incident not found")

// LeadUp is how far back the report looks for changes.
const LeadUp = 24 * time.Hour

// Coverage is what the check was and so what it can see.
type Coverage struct {
	Type     string        `json:"type"`
	Target   string        `json:"target"`
	Depth    string        `json:"depth,omitempty"`
	Interval time.Duration `json:"interval_ns,omitempty"`
	Timeout  time.Duration `json:"timeout_ns,omitempty"`
	Attempts int           `json:"attempts,omitempty"`
	Nodes    []string      `json:"nodes,omitempty"` // empty: checked from this server only
	Quorum   string        `json:"quorum,omitempty"`
	Policy   string        `json:"policy,omitempty"`
	Known    bool          `json:"known"` // false: the monitor is no longer configured
}

// Entry is one line of a segment.
type Entry struct {
	At   time.Time `json:"at"`
	What string    `json:"what"`
	Mark string    `json:"mark,omitempty"` // "warn" for lines worth a second look
}

// Segment is one of the four parts of the story.
type Segment struct {
	Key     string    `json:"key"` // lead_up | detection | notification | recovery
	Title   string    `json:"title"`
	From    time.Time `json:"from,omitzero"`
	To      time.Time `json:"to,omitzero"`
	Entries []Entry   `json:"entries"`
}

// Gap is a stretch with no check results.
type Gap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Liveness says whether monitoring itself was working.
type Liveness struct {
	Checked  bool      `json:"checked"` // false: no raw history left to judge
	Rounds   int       `json:"rounds"`
	Gaps     []Gap     `json:"gaps"`
	Events   []Entry   `json:"events"` // restarts, crashes, clock jumps, local outages
	Watched  bool      `json:"watched"`
	From, To time.Time `json:"-"`
}

// Report is the postmortem of one incident.
type Report struct {
	Lang       string              `json:"lang"`
	Generated  time.Time           `json:"generated"`
	Incident   store.IncidentRow   `json:"incident"`
	Class      string              `json:"class,omitempty"`
	Evidence   map[string]any      `json:"evidence,omitempty"`
	FailedAt   time.Time           `json:"failed_at,omitzero"`   // first failing round
	DetectedAt time.Time           `json:"detected_at,omitzero"` // failure confirmed (incident opened)
	NotifiedAt time.Time           `json:"notified_at,omitzero"` // first problem notice delivered
	AckedAt    time.Time           `json:"acked_at,omitzero"`
	ResolvedAt time.Time           `json:"resolved_at,omitzero"`
	Recipients []string            `json:"recipients,omitempty"`
	Segments   []Segment           `json:"segments"`
	Changes    []store.AuditEntry  `json:"config_changes"`
	Liveness   Liveness            `json:"liveness"`
	Coverage   Coverage            `json:"coverage"`
	Decisions  []engine.Decision   `json:"decisions"`
	Deliveries []store.DeliveryRow `json:"deliveries"`
	// The liveness and coverage sections as the report words them.
	LivenessText []string `json:"liveness_text"`
	CoverageText []string `json:"coverage_text"`
	Conclusion   string   `json:"conclusion"`
	loc          *time.Location
}

// Options of a report.
type Options struct {
	Lang     string
	Location *time.Location
	Now      time.Time
	Coverage Coverage
	// OwnChangesOnly keeps the changes that touched this monitor and leaves
	// out the rest of the configuration's history (a report read by a team
	// member must not show what other teams changed).
	OwnChangesOnly bool
}

func (o Options) t(key string, args ...any) string { return i18n.T(o.Lang, "pm."+key, args...) }

// Build assembles the report of incident id.
func Build(st *store.Store, id int64, o Options) (*Report, error) {
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	inc, err := st.Incident(id)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, ErrNotFound
	}
	r := &Report{Lang: o.Lang, Generated: o.Now, Incident: *inc, Coverage: o.Coverage, loc: o.Location}
	s := inc.State
	r.DetectedAt, r.ResolvedAt, r.AckedAt = s.OpenedAt, s.ResolvedAt, s.FirstAckedAt
	end := s.ResolvedAt
	if end.IsZero() {
		end = o.Now
	}

	// Rounds around the incident: when it really began, and whether the
	// checks kept running.
	var rounds []store.RoundRow
	if inc.Monitor != "" {
		rounds, _ = st.Rounds(inc.Monitor, s.OpenedAt.Add(-6*time.Hour), end.Add(10*time.Minute), 20000, inc.Site, inc.SourceEpoch)
	}
	r.FailedAt = failureStart(rounds, s.OpenedAt)

	decisions, _ := st.Decisions(id, inc.Monitor, r.FailedAt.Add(-LeadUp), end.Add(10*time.Minute))
	r.Decisions = decisions
	for _, d := range decisions {
		if d.Step == engine.LedQueued && d.Class != "" && r.Class == "" {
			r.Class, r.Evidence = d.Class, d.Evidence
		}
	}
	r.Deliveries, _ = st.DeliveriesOf(id)
	for _, d := range r.Deliveries {
		if !d.OK || r.NotifiedAt.After(time.Time{}) {
			continue
		}
		switch engine.NoticeKind(d.Kind) {
		case engine.NoticeProblem, engine.NoticeRepeat, engine.NoticeReopen, engine.NoticeEscalate, "SYSTEM", "DIGEST":
			r.NotifiedAt = d.At
			for _, a := range d.Attempts {
				if a.OK && !slices.Contains(r.Recipients, a.Channel) {
					r.Recipients = append(r.Recipients, a.Channel)
				}
			}
		}
	}
	// A digest that stood for this incident counts too.
	for _, d := range decisions {
		if d.Step == engine.LedSent && d.IncidentID != id && r.NotifiedAt.IsZero() {
			r.NotifiedAt = d.At
		}
	}

	changes := r.leadUpChanges(st)
	if o.OwnChangesOnly {
		var own []store.AuditEntry
		for _, a := range changes {
			if mentions(a, r.Incident.Monitor) {
				own = append(own, ownPart(a, r.Incident.Monitor))
			}
		}
		changes = own
	}
	r.Changes = changes
	r.Liveness = liveness(st, rounds, r.FailedAt.Add(-time.Hour), end, o.Coverage.Interval, o, inc.Site, inc.SourceEpoch)

	r.Segments = []Segment{
		r.leadUp(o, rounds, changes),
		r.detection(o, rounds),
		r.notification(o),
		r.recovery(o, end),
	}
	r.Conclusion = r.conclude(o)
	r.LivenessText, r.CoverageText = r.livenessLines(), r.coverageLines()
	return r, nil
}

// failureStart walks back from the confirmation to the first failing
// round of the run that led to it.
func failureStart(rounds []store.RoundRow, opened time.Time) time.Time {
	start := opened
	for i := len(rounds) - 1; i >= 0; i-- {
		rr := rounds[i]
		if rr.At.After(opened) {
			continue
		}
		if rr.Verdict != "critical" && rr.Status != "critical" {
			break
		}
		start = rr.At
	}
	return start
}

func (r *Report) at(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(r.loc).Format("2006-01-02 15:04:05 MST")
}

func (r *Report) dur(d time.Duration) string {
	if d < time.Second {
		d = 0
	}
	return i18n.Duration(d.Round(time.Second), r.Lang)
}

// leadUpChanges are the configuration and operational changes of the day
// before: the first thing to rule out.
func (r *Report) leadUpChanges(st *store.Store) []store.AuditEntry {
	from, to := r.FailedAt.Add(-LeadUp), r.FailedAt
	all, _ := st.AuditLog(store.AuditQuery{From: from, To: to, Limit: 1000})
	var out []store.AuditEntry
	for _, a := range all {
		switch {
		case strings.HasPrefix(a.Action, "session."), strings.HasPrefix(a.Action, "system."), strings.HasPrefix(a.Action, "audit."),
			a.Action == "backup.download", strings.HasPrefix(a.Action, "export."):
			continue
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b store.AuditEntry) int { return a.At.Compare(b.At) })
	return out
}

// mentions reports whether an audit entry touched the monitor.
// ownPart is the part of an entry that concerns one monitor: its own
// record changes from the diff, and no other detail of the entry, which
// can name the other records changed in the same action.
func ownPart(a store.AuditEntry, monitor string) store.AuditEntry {
	kept := []any{}
	if diff, ok := a.Detail["diff"].([]any); ok {
		for _, d := range diff {
			if m, ok := d.(map[string]any); ok && m["record"] == monitor {
				kept = append(kept, m)
			}
		}
	}
	a.Detail = map[string]any{"diff": kept}
	// The entry now describes this monitor only; a file-wide target such
	// as monitors.conf would say the whole configuration was in view.
	a.Target = monitor
	return a
}

func mentions(a store.AuditEntry, monitor string) bool {
	if monitor == "" {
		return false
	}
	if a.Target == monitor {
		return true
	}
	if diff, ok := a.Detail["diff"].([]any); ok {
		for _, d := range diff {
			if m, ok := d.(map[string]any); ok && m["record"] == monitor {
				return true
			}
		}
	}
	return false
}

func (r *Report) leadUp(o Options, rounds []store.RoundRow, changes []store.AuditEntry) Segment {
	seg := Segment{Key: "lead_up", Title: o.t("seg.lead_up"), From: r.FailedAt.Add(-LeadUp), To: r.FailedAt}
	for _, a := range changes {
		what := a.Action
		if a.Target != "" {
			what += " " + a.Target
		}
		what = o.t("change", "actor", a.Actor, "what", what)
		mark := ""
		if mentions(a, r.Incident.Monitor) {
			what += " — " + o.t("touches_monitor")
			mark = "warn"
		} else if a.Risk == "high" {
			mark = "warn"
		}
		seg.Entries = append(seg.Entries, Entry{At: a.At, What: what, Mark: mark})
	}
	for _, ev := range r.Liveness.Events {
		if ev.At.Before(r.FailedAt) {
			seg.Entries = append(seg.Entries, ev)
		}
	}
	var lastOK time.Time
	for _, rr := range rounds {
		if rr.At.Before(r.FailedAt) && rr.Verdict == "ok" {
			lastOK = rr.At
		}
	}
	if !lastOK.IsZero() {
		seg.Entries = append(seg.Entries, Entry{At: lastOK, What: o.t("last_ok")})
	}
	if len(seg.Entries) == 0 {
		seg.Entries = append(seg.Entries, Entry{What: o.t("no_changes")})
	}
	sortEntries(seg.Entries)
	return seg
}

func (r *Report) detection(o Options, rounds []store.RoundRow) Segment {
	seg := Segment{Key: "detection", Title: o.t("seg.detection"), From: r.FailedAt, To: r.DetectedAt}
	inc := r.Incident
	reason := i18n.T(o.Lang, "reason."+inc.Reason)
	if d := i18n.Detail(o.Lang, inc.Detail); d != "" {
		reason += " — " + d
	}
	n := 0
	for _, rr := range rounds {
		if !rr.At.Before(r.FailedAt) && !rr.At.After(r.DetectedAt) {
			n++
		}
	}
	seg.Entries = append(seg.Entries, Entry{At: r.FailedAt, What: o.t("first_failure", "reason", reason)})
	if n > 0 {
		seg.Entries = append(seg.Entries, Entry{At: r.DetectedAt, What: o.t("confirmed", "n", n, "after", r.dur(r.DetectedAt.Sub(r.FailedAt)))})
	} else {
		seg.Entries = append(seg.Entries, Entry{At: r.DetectedAt, What: o.t("opened")})
	}
	if r.Class != "" {
		seg.Entries = append(seg.Entries, Entry{At: r.DetectedAt, What: o.t("class", "class", o.t("class."+r.Class), "evidence", evidenceText(r.Evidence))})
	}
	for _, d := range r.Decisions {
		switch d.Step {
		case engine.LedHeld, engine.LedReleased:
			what := o.t("step."+d.Step, "from", r.at(d.HoldStart), "to", r.at(d.HoldEnd))
			if d.Note != "" {
				what += " (" + d.Note + ")"
			}
			seg.Entries = append(seg.Entries, Entry{At: d.At, What: what, Mark: "warn"})
		}
	}
	sortEntries(seg.Entries)
	return seg
}

func (r *Report) notification(o Options) Segment {
	seg := Segment{Key: "notification", Title: o.t("seg.notification"), From: r.DetectedAt, To: r.NotifiedAt}
	for _, d := range r.Decisions {
		switch d.Step {
		case engine.LedHeld, engine.LedReleased:
			continue
		}
		what := o.t("step."+d.Step, "kind", d.Kind, "to", strings.Join(d.Recipients, ", "))
		if len(d.Receipts) > 0 {
			var parts []string
			for _, a := range d.Receipts {
				parts = append(parts, receipt(a))
			}
			what += ": " + strings.Join(parts, "; ")
		}
		if d.Note != "" {
			what += " (" + d.Note + ")"
		}
		mark := ""
		switch d.Step {
		case engine.LedFailed, engine.LedDropped, engine.LedSuppressed, engine.LedBackstop, engine.LedMerged:
			mark = "warn"
		}
		seg.Entries = append(seg.Entries, Entry{At: d.At, What: what, Mark: mark})
	}
	if len(r.Decisions) == 0 {
		// Recorded before the ledger existed: the delivery log still tells.
		for _, d := range r.Deliveries {
			key := "step.sent"
			if !d.OK {
				key = "step.failed"
			}
			what := o.t(key, "kind", d.Kind, "to", channels(d.Attempts))
			seg.Entries = append(seg.Entries, Entry{At: d.At, What: what, Mark: map[bool]string{false: "warn"}[d.OK]})
		}
	}
	for _, a := range r.Incident.Acks {
		what := o.t("ack."+a.Action, "by", a.By)
		if a.Comment != "" {
			what += ": " + a.Comment
		}
		seg.Entries = append(seg.Entries, Entry{At: a.At, What: what})
	}
	if r.NotifiedAt.IsZero() {
		seg.Entries = append(seg.Entries, Entry{What: o.t("never_told"), Mark: "warn"})
	} else {
		seg.Entries = append(seg.Entries, Entry{At: r.NotifiedAt, What: o.t("told_after", "after", r.dur(r.NotifiedAt.Sub(r.DetectedAt)), "to", strings.Join(r.Recipients, ", "))})
	}
	sortEntries(seg.Entries)
	return seg
}

func (r *Report) recovery(o Options, end time.Time) Segment {
	seg := Segment{Key: "recovery", Title: o.t("seg.recovery"), From: r.ResolvedAt, To: r.ResolvedAt}
	if r.ResolvedAt.IsZero() {
		seg.Entries = append(seg.Entries, Entry{At: end, What: o.t("still_open", "for", r.dur(end.Sub(r.FailedAt))), Mark: "warn"})
		return seg
	}
	seg.Entries = append(seg.Entries, Entry{At: r.ResolvedAt, What: o.t("recovered", "for", r.dur(r.ResolvedAt.Sub(r.FailedAt)))})
	if r.Incident.State.ReopenCount > 0 {
		seg.Entries = append(seg.Entries, Entry{At: r.ResolvedAt, What: o.t("reopened", "n", r.Incident.State.ReopenCount), Mark: "warn"})
	}
	for _, d := range r.Deliveries {
		if engine.NoticeKind(d.Kind) == engine.NoticeRecovery {
			key := "step.sent"
			if !d.OK {
				key = "step.failed"
			}
			seg.Entries = append(seg.Entries, Entry{At: d.At, What: o.t(key, "kind", d.Kind, "to", channels(d.Attempts))})
		}
	}
	sortEntries(seg.Entries)
	return seg
}

func receipt(a notify.Attempt) string {
	s := a.Channel
	switch {
	case a.OK:
		s += " ✓"
	case a.Error != "":
		s += " ✗ " + a.Error
	default:
		s += " ✗"
	}
	if a.Status != 0 {
		s += fmt.Sprintf(" (%d)", a.Status)
	}
	return s
}

func channels(as []notify.Attempt) string {
	var out []string
	for _, a := range as {
		out = append(out, receipt(a))
	}
	return strings.Join(out, "; ")
}

func evidenceText(ev map[string]any) string {
	var keys []string
	for k := range ev {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, ev[k]))
	}
	return strings.Join(parts, ", ")
}

func sortEntries(es []Entry) {
	slices.SortStableFunc(es, func(a, b Entry) int {
		if a.At.IsZero() != b.At.IsZero() {
			return cmp.Compare(boolInt(a.At.IsZero()), boolInt(b.At.IsZero()))
		}
		return a.At.Compare(b.At)
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// liveness looks for stretches without check results and for what
// happened to the monitoring server itself.
func liveness(st *store.Store, rounds []store.RoundRow, from, to time.Time, interval time.Duration, o Options, sites ...string) Liveness {
	l := Liveness{From: from, To: to, Gaps: []Gap{}, Events: []Entry{}}
	evs, _ := st.EventsBetween(from.Add(-LeadUp), to, []string{engine.EventSystem}, 2000, sites...)
	for _, ev := range evs {
		code, _ := ev.Data["msg"].(string)
		if code == "" {
			code, _ = ev.Data["audit"].(string)
		}
		switch code {
		case "restarted", "abnormal_exit", "clock_changed", "hold_released", "outage_summary", "outage_backdated", "system_opened", "system_resolved":
		default:
			continue
		}
		mark := ""
		if code == "abnormal_exit" || code == "clock_changed" || code == "hold_released" {
			mark = "warn"
		}
		l.Events = append(l.Events, Entry{At: ev.At, What: o.t("sys_event", "what", ev.Message), Mark: mark})
	}
	var in []store.RoundRow
	for _, rr := range rounds {
		if !rr.At.Before(from) && !rr.At.After(to) {
			in = append(in, rr)
		}
	}
	l.Rounds = len(in)
	if len(in) == 0 {
		return l // nothing left to judge from
	}
	l.Checked = true
	if interval <= 0 {
		// Without the configuration, the typical spacing stands in.
		var ds []time.Duration
		for i := 1; i < len(in); i++ {
			ds = append(ds, in[i].At.Sub(in[i-1].At))
		}
		if len(ds) > 0 {
			slices.Sort(ds)
			interval = ds[len(ds)/2]
		}
	}
	limit := max(3*interval, 30*time.Second)
	prev := from
	for _, rr := range in {
		if rr.At.Sub(prev) > limit {
			l.Gaps = append(l.Gaps, Gap{From: prev, To: rr.At})
		}
		prev = rr.At
	}
	if to.Sub(prev) > limit {
		l.Gaps = append(l.Gaps, Gap{From: prev, To: to})
	}
	// The first stretch before the window's first round is a gap only
	// when the monitor already existed: the rounds tell.
	if len(l.Gaps) > 0 && l.Gaps[0].From.Equal(from) && len(rounds) > 0 && !rounds[0].At.Before(from) {
		l.Gaps = l.Gaps[1:]
	}
	l.Watched = len(l.Gaps) == 0
	return l
}

func (r *Report) conclude(o Options) string {
	inc := r.Incident
	name := inc.Monitor
	if name == "" {
		name = inc.Title
	}
	var b []string
	if r.ResolvedAt.IsZero() {
		b = append(b, o.t("c.open", "monitor", name, "since", r.at(r.FailedAt)))
	} else {
		b = append(b, o.t("c.down", "monitor", name, "from", r.at(r.FailedAt), "to", r.at(r.ResolvedAt), "for", r.dur(r.ResolvedAt.Sub(r.FailedAt))))
	}
	if r.Class != "" {
		b = append(b, o.t("c.class", "class", o.t("class."+r.Class)))
	}
	if r.NotifiedAt.IsZero() {
		b = append(b, o.t("c.not_told"))
	} else {
		b = append(b, o.t("c.told", "after", r.dur(r.NotifiedAt.Sub(r.DetectedAt)), "total", r.dur(r.NotifiedAt.Sub(r.FailedAt))))
	}
	n := 0
	for _, a := range r.Changes {
		if mentions(a, inc.Monitor) {
			n++
		}
	}
	if n > 0 {
		b = append(b, o.t("c.changed", "n", n))
	}
	switch {
	case !r.Liveness.Checked:
		b = append(b, o.t("c.live_unknown"))
	case r.Liveness.Watched:
		b = append(b, o.t("c.live_ok"))
	default:
		var total time.Duration
		for _, g := range r.Liveness.Gaps {
			total += g.To.Sub(g.From)
		}
		b = append(b, o.t("c.live_gaps", "n", len(r.Liveness.Gaps), "total", r.dur(total)))
	}
	return strings.Join(b, " ")
}
