package engine

import (
	"cmp"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
)

// The limit is what keeps one failure from becoming a flood of messages:
// no person is sent more than notify.backstop-max alerts in
// notify.backstop-window. A person is someone in the contact directory
// (their phone and e-mail are one person), an address the directory does
// not know, or a chat room or webhook (one inbox for everyone in it). An
// alert goes to everyone it is for at once or to no one: when any of its
// recipients has had their share, the whole alert is held, and the held
// alerts go out together as one summary through the same channels once a
// window has passed since the first of them, listing every one of them by
// address, so nothing is lost and the pattern shows (a switch shows as a
// run of neighbouring addresses). Problems, reminders, escalations and
// recoveries all count, each alert once per person however many channels
// carry it. Only messages about the system itself, drills, tests,
// reports and the summaries are never held.

type backstopBuf struct {
	first time.Time
	ins   []*Intent
}

// backstopExempt reports whether an alert is never held.
func backstopExempt(in *Intent) bool {
	if in.System || in.Message.DryRun {
		return true
	}
	switch NoticeKind(in.Kind) {
	case NoticeProblem, NoticeRepeat, NoticeReopen, NoticeEscalate, NoticeRecovery:
		return false
	}
	return true // digests, reports, drills, tests
}

// untouched reports whether no channel has been tried for an alert yet.
func untouched(in *Intent) bool {
	for _, st := range in.Delivery.Steps {
		if st.Attempts > 0 {
			return false
		}
	}
	return true
}

// recipients lists who a channel reaches: its people, or the channel
// itself; a contact group reaches its members' people.
func (e *Engine) recipients(ch string, depth int) []string {
	c, ok := e.cfg.Channels[ch]
	if !ok {
		return nil
	}
	if len(c.Members) > 0 && depth < 3 {
		var out []string
		for _, m := range c.Members {
			for _, r := range e.recipients(m, depth+1) {
				if !slices.Contains(out, r) {
					out = append(out, r)
				}
			}
		}
		return out
	}
	if len(c.Recipients) > 0 {
		return c.Recipients
	}
	return []string{"channel:" + ch}
}

// planned are the channels an untouched alert goes to first: all of them,
// or the head of a fallback chain.
func planned(in *Intent) []string {
	var out []string
	for _, st := range in.Delivery.Steps {
		out = append(out, st.Channel)
		if in.Delivery.Mode != DeliverAll {
			break
		}
	}
	return out
}

// countRecipients counts an alert against the people a channel reaches,
// each person once per alert.
func (e *Engine) countRecipients(in *Intent, ch string, now time.Time) {
	if in.Message.DryRun {
		return
	}
	if in.counted == nil {
		in.counted = map[string]bool{}
	}
	for _, r := range e.recipients(ch, 0) {
		if !in.counted[r] {
			in.counted[r] = true
			e.noteSent(r, now)
		}
	}
}

// noteSent counts an alert sent to one recipient.
func (e *Engine) noteSent(ch string, now time.Time) {
	win := e.cfg.Tunables.Duration("notify.backstop-window")
	ts := e.sentAt[ch]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= win {
		i++
	}
	e.sentAt[ch] = append(ts[i:], now)
}

// overBackstop reports whether a recipient has had their share this
// window.
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

// backstop holds an alert when anyone it goes to has had their share; it
// reports whether it did.
func (e *Engine) backstop(in *Intent, now time.Time) bool {
	if backstopExempt(in) || len(in.Delivery.Steps) == 0 || !untouched(in) {
		return false
	}
	full := ""
	for _, ch := range planned(in) {
		for _, r := range e.recipients(ch, 0) {
			if full == "" && e.overBackstop(r, now) {
				full = r
			}
		}
	}
	if full == "" {
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
	e.decideIntent(in, LedBackstop, "more than notify.backstop-max alerts to "+full+" this window: held for the summary")
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
	// Every alert, ordered by where its target is: neighbours stay
	// together, so a switch or a link shows as one run of addresses.
	list := slices.Clone(ins)
	slices.SortStableFunc(list, func(a, b *Intent) int {
		if c := byTarget(a.Message.Target, b.Message.Target); c != 0 {
			return c
		}
		return cmp.Compare(a.Message.Monitor, b.Message.Monitor)
	})
	lines := []string{e.text("backstop_body", len(ins), len(mons))}
	for _, in := range list {
		line := "- " + digestLine(in)
		if t := in.Message.Target; t != "" {
			line += " · " + t
		}
		lines = append(lines, line)
	}
	d := &Intent{Kind: "DIGEST", Policy: pol, CreatedAt: now, DueAt: now, Covers: ids,
		Message: notify.Message{Kind: "DIGEST", Priority: "high", Title: e.text("backstop_title", len(ins)),
			Body: e.text("digest_body", lines, e.cfg.BaseURL), Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang}}
	if e.enqueue(d) {
		e.decide(Decision{At: now, IntentID: d.ID, Kind: "DIGEST", Step: LedQueued, Recipients: d.Delivery.Channels(),
			Note: fmt.Sprintf("backstop digest of %d folded alerts", len(ins))})
	}
}

// byTarget orders targets by address, then port; targets that name no
// address come after the ones that do.
func byTarget(a, b string) int {
	aa, ap, aok := targetAddr(a)
	ba, bp, bok := targetAddr(b)
	switch {
	case aok && bok:
		if c := aa.Compare(ba); c != 0 {
			return c
		}
		return cmp.Compare(ap, bp)
	case aok:
		return -1
	case bok:
		return 1
	}
	return 0
}

// targetAddr is the IP address and port in a monitor's target, if it
// names an address ("10.0.0.5:80", "https://10.0.0.5/",
// "[2001:db8::1]:443"); the port is 0 when none is given.
func targetAddr(t string) (netip.Addr, int, bool) {
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	if i := strings.IndexAny(t, "/?#"); i >= 0 {
		t = t[:i]
	}
	port := 0
	if h, p, err := net.SplitHostPort(t); err == nil {
		t = h
		port, _ = strconv.Atoi(p)
	}
	a, err := netip.ParseAddr(strings.Trim(t, "[]"))
	if err != nil {
		return netip.Addr{}, 0, false
	}
	return a.Unmap(), port, true
}
