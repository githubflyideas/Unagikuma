package engine

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
)

// The backstop is the last guard against a flood: no channel gets more
// than notify.backstop-max alerts per notify.backstop-window. Further
// alerts are folded and sent as one digest when the window has passed,
// naming how many there were, the monitors involved and the worst ones.
// Escalations, recoveries, system notices and digests always go through:
// a recovery is the message that lets people stop working on a problem,
// so holding it back would keep them busy for nothing. The first
// alert of a new incident is not held back by repeats and reminders: new
// incidents have a share of their own, so a new problem goes out at once
// unless notify.backstop-max new problems already went out this window —
// a hundred services failing together is one digest, not a hundred pages.

type backstopBuf struct {
	first time.Time
	ins   []*Intent
}

// backstopExempt reports whether an alert is never folded.
func backstopExempt(in *Intent) bool {
	if in.System || in.Message.DryRun {
		return true
	}
	switch NoticeKind(in.Kind) {
	case NoticeEscalate, NoticeRecovery:
		return true
	case NoticeProblem, NoticeRepeat, NoticeReopen:
		return false
	}
	return true // digests, reports, drills, tests
}

// newProblem reports whether an alert is the first of a new incident.
func newProblem(in *Intent) bool { return NoticeKind(in.Kind) == NoticeProblem && in.Seq <= 1 }

// backstopKey is what an alert counts against: the channel, or the
// channel's share for new incidents.
func backstopKey(in *Intent, ch string) string {
	if newProblem(in) {
		return "new:" + ch
	}
	return ch
}

// noteSent counts a send to a channel (or its share for new incidents) for
// the backstop.
func (e *Engine) noteSent(ch string, now time.Time) {
	win := e.cfg.Tunables.Duration("notify.backstop-window")
	ts := e.sentAt[ch]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= win {
		i++
	}
	e.sentAt[ch] = append(ts[i:], now)
}

// overBackstop reports whether a channel has had its share this window.
func (e *Engine) overBackstop(ch string, now time.Time) bool {
	limit := e.cfg.Tunables.Int("notify.backstop-max")
	if limit <= 0 {
		return false
	}
	win := e.cfg.Tunables.Duration("notify.backstop-window")
	n := 0
	for _, t := range e.sentAt[ch] {
		if now.Sub(t) < win {
			n++
		}
	}
	return n >= limit
}

// backstop folds an alert whose first channel is over its share; it
// reports whether it did.
func (e *Engine) backstop(in *Intent, now time.Time) bool {
	if backstopExempt(in) || len(in.Delivery.Steps) == 0 || in.Delivery.Pos() > 0 || in.Delivery.Steps[0].Attempts > 0 {
		return false
	}
	ch := in.Delivery.Steps[0].Channel
	if !e.overBackstop(backstopKey(in, ch), now) {
		return false
	}
	key := in.Policy
	b := e.folded[key]
	if b == nil {
		b = &backstopBuf{first: now}
		e.folded[key] = b
	}
	b.ins = append(b.ins, in)
	in.State, in.UpdatedAt = IntentMerged, now
	_ = e.deps.Sink.SaveIntent(in)
	delete(e.intents, in.ID)
	e.decideIntent(in, LedBackstop, "more than notify.backstop-max alerts to "+ch+" this window: folded into a digest")
	return true
}

// flushBackstop sends the digest of each policy whose folded alerts have
// waited a whole window.
func (e *Engine) flushBackstop(now time.Time) {
	win := e.cfg.Tunables.Duration("notify.backstop-window")
	for _, pol := range slices.Sorted(maps.Keys(e.folded)) {
		b := e.folded[pol]
		if now.Sub(b.first) < win {
			continue
		}
		delete(e.folded, pol)
		e.sendBackstop(pol, b.ins, now)
	}
}

func (e *Engine) sendBackstop(pol string, ins []*Intent, now time.Time) {
	var ids []int64
	var mons []string
	for _, in := range ins {
		switch NoticeKind(in.Kind) {
		case NoticeProblem, NoticeRepeat, NoticeReopen:
			if in.IncidentID != 0 {
				ids = append(ids, in.IncidentID)
			}
		}
		if m := in.Message.Monitor; m != "" && !slices.Contains(mons, m) {
			mons = append(mons, m)
		}
	}
	// Worst first: problems before recoveries, high priority before low,
	// longest down first.
	worst := slices.Clone(ins)
	rank := func(in *Intent) int {
		r := 0
		if NoticeKind(in.Kind) != NoticeRecovery {
			r += 2
		}
		if in.Message.Priority == "high" {
			r++
		}
		return r
	}
	slices.SortStableFunc(worst, func(a, b *Intent) int {
		if c := cmp.Compare(rank(b), rank(a)); c != 0 {
			return c
		}
		return cmp.Compare(b.Message.Duration, a.Message.Duration)
	})
	lines := []string{e.text("backstop_body", len(ins), len(mons), shortList(mons, 10))}
	for _, in := range worst[:min(5, len(worst))] {
		lines = append(lines, "- "+digestLine(in))
	}
	d := &Intent{Kind: "DIGEST", Policy: pol, CreatedAt: now, DueAt: now, Covers: ids,
		Message: notify.Message{Kind: "DIGEST", Priority: "high", Title: e.text("backstop_title", len(ins)),
			Body: e.text("digest_body", lines, e.cfg.BaseURL), Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang}}
	if e.enqueue(d) {
		e.decide(Decision{At: now, IntentID: d.ID, Kind: "DIGEST", Step: LedQueued, Recipients: d.Delivery.Channels(),
			Note: fmt.Sprintf("backstop digest of %d folded alerts", len(ins))})
	}
}

func shortList(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + ", …"
}
