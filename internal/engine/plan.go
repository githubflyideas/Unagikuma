package engine

import (
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Notification discipline for one incident: capped notifications, repeats,
// reopen within a window, ACK and silence, and the deferred "recovered,
// flapped N times" summary. Pure functions over IncidentState; the caller
// persists the state and turns PlannedNotices into delivery intents.

// NotifyPolicy is the resolved notification discipline of one policy.
type NotifyPolicy struct {
	Max              int // problem notifications per incident; 0 = unlimited
	Repeat           time.Duration
	Delay            time.Duration
	ReopenWindow     time.Duration
	Recovery         bool
	NotifyDegraded   bool
	DegradedMax      int
	AckExpire        time.Duration // 0 = ACKs never expire
	AckClearOnChange bool
	RecoveryAfterAck bool
}

// DefaultNotifyPolicy returns the policy used when a POLICY record leaves
// fields unset: thresholds come from the tunables, switches from the
// documented defaults.
func DefaultNotifyPolicy(t tunables.Set) NotifyPolicy {
	return NotifyPolicy{
		Max:              t.Int("notify.max"),
		Repeat:           t.Duration("notify.repeat"),
		Delay:            t.Duration("notify.delay"),
		ReopenWindow:     t.Duration("notify.reopen-window"),
		Recovery:         true,
		DegradedMax:      t.Int("notify.degraded-max"),
		AckClearOnChange: true,
		RecoveryAfterAck: true,
	}
}

func (p NotifyPolicy) max(low bool) int {
	if low {
		return p.DegradedMax
	}
	return p.Max
}

// NoticeKind is the kind of a planned notification.
type NoticeKind string

const (
	NoticeProblem  NoticeKind = "PROBLEM"
	NoticeRepeat   NoticeKind = "REPEAT"
	NoticeReopen   NoticeKind = "REOPEN"
	NoticeRecovery NoticeKind = "RECOVERY"
	NoticeEscalate NoticeKind = "ESCALATE"
)

// PlannedNotice is a notification the incident owes right now.
type PlannedNotice struct {
	Kind NoticeKind
	// Seq is the problem notification number (1-based) for PROBLEM, REPEAT
	// and REOPEN; for RECOVERY it is the number of problem notifications sent.
	Seq int
	Max int // cap in force (0 = unlimited)
	// Flaps is the number of reopens folded into a RECOVERY summary.
	Flaps int
}

// Incident lifecycle events worth recording.
const (
	IncidentCapped       = "capped"
	IncidentAckExpired   = "ack_expired"
	IncidentAckCleared   = "ack_cleared"
	IncidentSilenceEnded = "silence_ended"
	IncidentReopened     = "reopened"
	IncidentClosed       = "closed"
)

// IncidentState is the notification-relevant state of one incident.
type IncidentState struct {
	Low   bool `json:"low,omitzero"`   // DEGRADED low-priority incident
	Drill bool `json:"drill,omitzero"` // shadow incident of a drill

	OpenedAt   time.Time `json:"opened_at,omitzero"`
	HardAt     time.Time `json:"hard_at,omitzero"`     // latest entry into HARD
	ResolvedAt time.Time `json:"resolved_at,omitzero"` // zero while open
	Closed     bool      `json:"closed,omitzero"`      // reopen window passed after resolution

	NotifyCount int `json:"notify_count"`
	// Told counts the problem notifications actually delivered; Tracked is
	// set on incidents opened since it is kept (older ones count planned
	// notifications as delivered). Queued notices that never got out —
	// during a local outage, say — do not make anyone owed a recovery.
	Told         int       `json:"told,omitzero"`
	Tracked      bool      `json:"tracked,omitzero"`
	LastNotifyAt time.Time `json:"last_notify_at,omitzero"`
	Capped       bool      `json:"capped,omitzero"`

	Acked        bool      `json:"acked,omitzero"`
	AckedBy      string    `json:"acked_by,omitzero"`
	AckExpiresAt time.Time `json:"ack_expires_at,omitzero"`
	// FirstAckedAt is the first acknowledgement, kept when it is
	// withdrawn or repeated: the time to acknowledge is measured to it.
	FirstAckedAt time.Time `json:"first_acked_at,omitzero"`
	ReasonClass  string    `json:"reason_class"`

	SilencedUntil time.Time `json:"silenced_until,omitzero"`

	ReopenCount int `json:"reopen_count,omitzero"`
	// QuietFlaps counts reopens that were not notified (capped, ACKed or
	// silenced); they are reported once in the deferred summary.
	QuietFlaps int       `json:"quiet_flaps,omitzero"`
	SummaryDue time.Time `json:"summary_due,omitzero"`
	// Escalated counts the escalation steps already sent.
	Escalated int `json:"escalated,omitzero"`
	// Responders are, by on-call rotation, the member who last received a
	// problem notice of this incident: its recovery goes to them first,
	// even when the shift has changed hands or a stand-in has ended since.
	// Kept as "rotation=member,rotation=member" so the state stays
	// comparable.
	Responders string `json:"responders,omitzero"`
}

// Responder returns the member of rotation who last took a problem notice.
func (s *IncidentState) Responder(rotation string) string {
	for _, kv := range strings.Split(s.Responders, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == rotation {
			return v
		}
	}
	return ""
}

// setResponder records member as the responder of rotation.
func (s *IncidentState) setResponder(rotation, member string) {
	out := []string{rotation + "=" + member}
	for _, kv := range strings.Split(s.Responders, ",") {
		if k, _, ok := strings.Cut(kv, "="); ok && k != rotation {
			out = append(out, kv)
		}
	}
	sort.Strings(out)
	s.Responders = strings.Join(out, ",")
}

// told reports whether anyone received a problem notification.
func (s *IncidentState) told() bool {
	if !s.Tracked {
		return s.NotifyCount > 0
	}
	return s.Told > 0
}

// Delivered books a problem notification that reached someone.
func (s *IncidentState) Delivered() { s.Told++ }

// Open reports whether the incident is currently failing.
func (s *IncidentState) Open() bool { return !s.Closed && s.ResolvedAt.IsZero() }

func (s *IncidentState) quiet(now time.Time) bool {
	return s.Capped || s.Acked || now.Before(s.SilencedUntil)
}

// Reopenable reports whether a new HARD failure at now reopens s.
func (s *IncidentState) Reopenable(now time.Time, p NotifyPolicy) bool {
	return s != nil && !s.Closed && !s.ResolvedAt.IsZero() && now.Sub(s.ResolvedAt) < p.ReopenWindow
}

// Fail handles entry into HARD. With a reopenable prev it reopens it (same
// counters and cap); otherwise it returns a new incident. The caller sets
// Low/Drill on new incidents before the first Advance.
func Fail(prev *IncidentState, now time.Time, reasonClass string, p NotifyPolicy) (s *IncidentState, notices []PlannedNotice, events []string) {
	if !prev.Reopenable(now, p) {
		s = &IncidentState{OpenedAt: now, HardAt: now, ReasonClass: reasonClass, Tracked: true}
		notices, events = Advance(s, now, p)
		return s, notices, events
	}
	s = prev
	upFor := now.Sub(s.ResolvedAt)
	s.ResolvedAt, s.SummaryDue = time.Time{}, time.Time{}
	s.HardAt = now
	s.ReopenCount++
	events = append(events, IncidentReopened)
	if ReasonChanged(s, reasonClass, p) {
		events = append(events, IncidentAckCleared)
	}
	max := p.max(s.Low)
	told := s.NotifyCount > 0
	if told && upFor >= p.ReopenWindow/2 {
		// It was really back (at least half the reopen window): this
		// failure counts its own notifications again ("1 of 3"), so a
		// service that fails now and then never runs out of alerts. Quick
		// flaps keep counting and fold into the summary once capped.
		s.NotifyCount, s.Capped = 0, false
	}
	switch {
	case !told:
		// Nobody was told about the first failure (it recovered within the
		// delay): start over as if it were new.
		n, e := Advance(s, now, p)
		notices, events = append(notices, n...), append(events, e...)
	case s.quiet(now):
		// Capped, ACKed or silenced: fold the flap into the summary.
		s.QuietFlaps++
	case max == 0 || s.NotifyCount < max:
		notices = append(notices, s.sent(NoticeReopen, now, max))
		if s.Capped {
			events = append(events, IncidentCapped)
		}
	}
	return s, notices, events
}

// sent books one problem notification.
func (s *IncidentState) sent(kind NoticeKind, now time.Time, max int) PlannedNotice {
	s.NotifyCount++
	s.LastNotifyAt = now
	if max > 0 && s.NotifyCount >= max {
		s.Capped = true
	}
	return PlannedNotice{Kind: kind, Seq: s.NotifyCount, Max: max}
}

// Advance applies time: ACK expiry, silence end, the delayed first
// notification, repeats, the deferred summary and closing. Call it on every
// engine tick for open or recently resolved incidents.
func Advance(s *IncidentState, now time.Time, p NotifyPolicy) (notices []PlannedNotice, events []string) {
	if s.Closed {
		return nil, nil
	}
	if s.Acked && !s.AckExpiresAt.IsZero() && !now.Before(s.AckExpiresAt) {
		s.Acked, s.AckedBy, s.AckExpiresAt = false, "", time.Time{}
		events = append(events, IncidentAckExpired)
	}
	if !s.SilencedUntil.IsZero() && !now.Before(s.SilencedUntil) {
		s.SilencedUntil = time.Time{}
		events = append(events, IncidentSilenceEnded)
	}
	if !s.ResolvedAt.IsZero() {
		if !s.SummaryDue.IsZero() && !now.Before(s.SummaryDue) {
			notices = append(notices, PlannedNotice{Kind: NoticeRecovery, Seq: s.NotifyCount, Max: p.max(s.Low), Flaps: s.QuietFlaps})
			s.SummaryDue, s.QuietFlaps = time.Time{}, 0
		}
		if now.Sub(s.ResolvedAt) >= p.ReopenWindow && s.SummaryDue.IsZero() {
			s.Closed = true
			events = append(events, IncidentClosed)
		}
		return notices, events
	}
	if s.quiet(now) {
		return notices, events
	}
	max := p.max(s.Low)
	if s.Low && !p.NotifyDegraded {
		return notices, events
	}
	switch {
	case s.NotifyCount == 0:
		if !now.Before(s.HardAt.Add(p.Delay)) {
			notices = append(notices, s.sent(NoticeProblem, now, max))
		}
	case (max == 0 || s.NotifyCount < max) && p.Repeat > 0 && !now.Before(s.LastNotifyAt.Add(p.Repeat)):
		notices = append(notices, s.sent(NoticeRepeat, now, max))
	}
	if len(notices) > 0 && s.Capped {
		events = append(events, IncidentCapped)
	}
	return notices, events
}

// Recover handles leaving HARD (OK or, for normal incidents, DEGRADED).
func Recover(s *IncidentState, now time.Time, p NotifyPolicy) []PlannedNotice {
	if !s.Open() {
		return nil
	}
	s.ResolvedAt = now
	switch {
	case s.Low, !s.told(), !p.Recovery:
		// Low-priority incidents and incidents nobody was told about end
		// silently; a pending delayed first notification is dropped.
		s.QuietFlaps = 0
		return nil
	case s.QuietFlaps > 0:
		// Flapping while quiet: one summary once it stayed fine for the
		// whole reopen window.
		s.SummaryDue = now.Add(p.ReopenWindow)
		return nil
	case s.Acked && !p.RecoveryAfterAck:
		return nil
	}
	return []PlannedNotice{{Kind: NoticeRecovery, Seq: s.NotifyCount, Max: p.max(s.Low)}}
}

// Ack acknowledges the incident; expire 0 uses the policy's AckExpire.
func Ack(s *IncidentState, by string, now time.Time, expire time.Duration, p NotifyPolicy) {
	if expire == 0 {
		expire = p.AckExpire
	}
	s.Acked, s.AckedBy, s.AckExpiresAt = true, by, time.Time{}
	if s.FirstAckedAt.IsZero() {
		s.FirstAckedAt = now
	}
	if expire > 0 {
		s.AckExpiresAt = now.Add(expire)
	}
}

// Unack withdraws an acknowledgement.
func Unack(s *IncidentState) { s.Acked, s.AckedBy, s.AckExpiresAt = false, "", time.Time{} }

// Silence suppresses notifications until the given time (zero lifts it).
func Silence(s *IncidentState, until time.Time) { s.SilencedUntil = until }

// ReasonChanged records the failure's reason class and clears the ACK when
// the class changed and the policy says so. It reports whether the ACK was
// cleared.
func ReasonChanged(s *IncidentState, class string, p NotifyPolicy) bool {
	changed := class != "" && s.ReasonClass != "" && class != s.ReasonClass
	if class != "" {
		s.ReasonClass = class
	}
	if changed && s.Acked && p.AckClearOnChange {
		Unack(s)
		return true
	}
	return false
}

// NextDaily returns the first hh:mm in loc strictly after t; used for the
// daily open-incident reminder and the daily report.
func NextDaily(t time.Time, hh, mm int, loc *time.Location) time.Time {
	lt := t.In(loc)
	next := time.Date(lt.Year(), lt.Month(), lt.Day(), hh, mm, 0, 0, loc)
	if !next.After(t) {
		next = time.Date(lt.Year(), lt.Month(), lt.Day()+1, hh, mm, 0, 0, loc)
	}
	return next
}
