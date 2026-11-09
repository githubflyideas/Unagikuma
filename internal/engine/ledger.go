package engine

import (
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/notify"
)

// The alert decision ledger records why each alert was sent, held, merged
// or dropped, so the question "why were we (not) told, and when?" has an
// answer for every incident: the classification and its evidence, the
// hold during a local outage, the people and channels it went to, the
// delivery receipts and the acknowledgement.

// Failure classes: what the evidence says the failure is.
const (
	ClassBusiness    = "business"     // the service answers, but wrongly: its owner acts
	ClassNetwork     = "network"      // the network fails (this side, or a whole segment)
	ClassUncertain   = "uncertain"    // cannot be reached: the service or the way to it
	ClassChannelDown = "channel_down" // the alert itself could not be delivered
	ClassSystem      = "system"       // about the monitoring itself
)

// Ledger steps.
const (
	LedQueued     = "queued"     // an alert was decided and queued
	LedSent       = "sent"       // delivered (receipts attached)
	LedFailed     = "failed"     // every channel failed
	LedHeld       = "held"       // held back by a local outage (hold start and end)
	LedReleased   = "released"   // the hold lasted too long and was released
	LedDropped    = "dropped"    // a queued alert was judged stale and dropped
	LedMerged     = "merged"     // merged into a summary (storm control)
	LedBackstop   = "backstop"   // too many for one recipient: folded into its digest
	LedExpired    = "expired"    // queued longer than notify.queue-ttl: replaced by a summary
	LedSuppressed = "suppressed" // not sent: maintenance window
	LedAcked      = "acked"
)

// Decision is one entry of the ledger.
type Decision struct {
	At         time.Time        `json:"at"`
	IncidentID int64            `json:"incident_id,omitempty"`
	IntentID   string           `json:"intent_id,omitempty"`
	Monitor    string           `json:"monitor,omitempty"`
	Kind       string           `json:"kind,omitempty"`
	Step       string           `json:"step"`
	Class      string           `json:"class,omitempty"`
	Evidence   map[string]any   `json:"evidence,omitempty"`
	Recipients []string         `json:"recipients,omitempty"`
	Receipts   []notify.Attempt `json:"receipts,omitempty"`
	DetectedAt time.Time        `json:"detected_at,omitzero"`
	HoldStart  time.Time        `json:"hold_start,omitzero"`
	HoldEnd    time.Time        `json:"hold_end,omitzero"`
	Note       string           `json:"note,omitempty"`
}

// DecisionSink is implemented by sinks that keep the ledger.
type DecisionSink interface {
	Decision(Decision)
}

func (e *Engine) decide(d Decision) {
	if s, ok := e.deps.Sink.(DecisionSink); ok {
		if d.At.IsZero() {
			d.At = e.now()
		}
		s.Decision(d)
	}
}

// decideIntent records a step of an alert.
func (e *Engine) decideIntent(in *Intent, step, note string) {
	d := Decision{IncidentID: in.IncidentID, IntentID: in.ID, Monitor: in.Message.Monitor, Kind: in.Kind, Step: step,
		Class: in.Message.Class, Note: note}
	if inc := e.incidents[in.IncidentID]; inc != nil {
		d.DetectedAt = inc.State.HardAt
	}
	switch step {
	case LedQueued:
		d.Evidence = in.Evidence
		d.Recipients = in.Delivery.Channels()
	case LedSent, LedFailed:
		d.Recipients, d.Receipts = in.Delivery.Channels(), in.Attempts
		if step == LedFailed {
			d.Class = ClassChannelDown
		}
	}
	e.decide(d)
}

// classify says what an incident's failure is, with the evidence.
func (e *Engine) classOf(inc *Incident) (string, map[string]any) {
	if inc.Kind == IncidentSystem {
		return ClassSystem, map[string]any{"system": inc.System}
	}
	ev := map[string]any{"reason": inc.Reason}
	if inc.Detail != "" {
		ev["detail"] = inc.Detail
	}
	rt := e.states[inc.Monitor]
	reason := inc.Reason
	if rt != nil {
		if rt.LastReason != "" {
			reason = rt.LastReason
		}
		if q := rt.LastVerdict.Summary(); q != "" {
			ev["quorum"] = q
		}
	}
	h := e.localnet.Health(e.now())
	ev["uplink"] = h.Uplink
	if h.GatewayState != "" {
		ev["gateway"] = h.GatewayState
	}
	if len(e.localnet.Active()) > 0 && rt != nil {
		if ex, _ := e.localnet.Affects(rt.Scope, famOr(rt.Family), rt.OnLink, rt.ByName); ex {
			ev["local_outage"] = e.localnet.Active()[0].Kind
			return ClassNetwork, ev
		}
	}
	switch reasonClass(reason) {
	case "network":
		// A whole segment failing at the network level points at the
		// network; one target alone could be the target or the path.
		if rt != nil && (strings.HasPrefix(rt.Domain, "4:") || strings.HasPrefix(rt.Domain, "6:")) {
			n := 0
			for name, o := range e.states {
				if name != inc.Monitor && o.Domain == rt.Domain && o.State.Avail == StatusCritical && reasonClass(o.LastReason) == "network" {
					n++
				}
			}
			ev["same_segment_failing"] = n
			if n+1 >= max(2, e.cfg.Tunables.Int("notify.group-min")) {
				ev["segment"] = rt.Domain[2:]
				return ClassNetwork, ev
			}
		}
		return ClassUncertain, ev
	case "tls", "protocol":
		if m := e.monitors[inc.Monitor]; m != nil && m.Push {
			return ClassUncertain, ev // a missed push: the job or the way to us
		}
		return ClassBusiness, ev
	}
	return ClassUncertain, ev
}

// classLine is the line that tells the recipient what kind of failure
// this is, and so who should act. Only problem notices carry it.
func (e *Engine) classLine(class string, kind NoticeKind, ev map[string]any) string {
	switch kind {
	case NoticeProblem, NoticeRepeat, NoticeReopen, NoticeEscalate:
	default:
		return ""
	}
	lang := e.cfg.Lang
	switch class {
	case ClassBusiness:
		return i18n.T(lang, "msg.class.business")
	case ClassUncertain:
		return i18n.T(lang, "msg.class.uncertain")
	case ClassNetwork:
		if seg, ok := ev["segment"].(string); ok {
			return i18n.T(lang, "msg.class.segment", "segment", seg, "n", ev["same_segment_failing"].(int)+1)
		}
		return i18n.T(lang, "msg.class.local")
	}
	return ""
}
