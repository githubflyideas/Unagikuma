package engine

import (
	"cmp"
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/ratelimit"
)

// attemptResult is the outcome of sending one intent to one channel.
type attemptResult struct {
	intent string
	step   int
	att    notify.Attempt
	// more are the member attempts of a group channel; att then sums
	// them up (OK when any member got the message).
	more []notify.Attempt
}

// pendingIntents returns undelivered intents, oldest first.
func (e *Engine) pendingIntents() []*Intent {
	out := make([]*Intent, 0, len(e.intents))
	for _, in := range e.intents {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// dispatchIntents starts the channel attempts that are due, honouring
// per-channel rate limits, pauses during local outages and storm control.
func (e *Engine) dispatchIntents(ctx context.Context, wg *sync.WaitGroup) {
	now := e.now()
	if len(e.folded) > 0 {
		e.flushBackstop(now)
	}
	if len(e.intents) == 0 {
		return
	}
	e.coalesce(now)
	paused := func(ch string) bool {
		c, ok := e.cfg.Channels[ch]
		if !ok {
			return false
		}
		ex, _ := e.localnet.Affects(c.Scope, famOr(c.Family), c.OnLink, c.ByName)
		return ex
	}
	timeout := e.cfg.Tunables.Duration("notify.channel-timeout")
	late := e.cfg.Tunables.Duration("notify.intent-late")
	for _, in := range e.pendingIntents() {
		if now.Before(in.DueAt) || e.backstop(in, now) {
			continue
		}
		steps, st := in.Delivery.Next(now, paused)
		switch st {
		case DeliveryDone, DeliveryFailed:
			e.finishIntent(in, now)
			continue
		}
		for _, i := range steps {
			name := in.Delivery.Steps[i].Channel
			if i < in.ForceFail {
				in.Delivery.Begin(i)
				in.Delivery.Finish(i, false, "forced failure (drill)", now, 0, 0)
				in.Attempts = append(in.Attempts, notify.Attempt{Channel: name, Error: "forced failure (drill)"})
				continue
			}
			ch, ok := e.cfg.Channels[name]
			if !ok {
				// Channel removed from the configuration: count it as failed.
				in.Delivery.Begin(i)
				in.Delivery.Finish(i, false, "channel not configured", now, 0, 0)
				continue
			}
			if ch.NoRecipients != "" {
				in.Delivery.Begin(i)
				in.Delivery.Finish(i, false, ch.NoRecipients, now, 0, 0)
				in.Attempts = append(in.Attempts, notify.Attempt{Channel: name, Error: ch.NoRecipients})
				continue
			}
			if in.Kind == "REPORT" {
				if ch.NoReport {
					in.Delivery.Skip(i)
					in.Attempts = append(in.Attempts, notify.Attempt{Channel: name, Error: "does not take the daily report"})
					continue
				}
				if ch.ReportOpts != nil {
					ch.Channel.Opts = ch.ReportOpts
				}
			}
			if !e.inPeriod(ch, now) {
				in.Delivery.Skip(i)
				in.Attempts = append(in.Attempts, notify.Attempt{Channel: name, Error: "outside its time period"})
				continue
			}
			if e.heldTooLong(in, i, name, now) {
				// The service told this channel to wait longer than an
				// alert should: the next channel takes it.
				in.Delivery.Begin(i)
				in.Delivery.Finish(i, false, "held by the service's rate limit", now, 0, 0)
				in.Attempts = append(in.Attempts, notify.Attempt{Channel: name, Error: "held by the service's rate limit"})
				continue
			}
			if !e.channelAllows(name, ch.Kind, now) {
				continue // rate limited: stays queued
			}
			in.Delivery.Begin(i)
			in.UpdatedAt = now
			if !in.Message.DryRun {
				e.noteSent(name, now)
				if newProblem(in) {
					e.noteSent(backstopKey(in, name), now)
				}
			}
			// Durable before the attempt: a crash now resends at most once.
			if err := e.deps.Sink.SaveIntent(in); err != nil {
				e.log.Error("cannot persist intent", "err", err)
			}
			msg := in.Message
			msg.Delayed = LateBy(in.DueAt, now, late)
			id, step := in.ID, i
			members := e.groupMembers(ch, now)
			wg.Add(1)
			go func() {
				defer wg.Done()
				sctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				res := attemptResult{intent: id, step: step}
				switch {
				case len(ch.Members) == 0:
					res.att = e.deps.Send(sctx, ch.Channel, &msg)
				default:
					res.att, res.more = e.sendGroup(sctx, members, &msg)
				}
				res.att.Channel = ch.Name
				select {
				case e.attempts <- res:
				case <-ctx.Done():
				}
			}()
		}
		if in.Delivery.Status() == DeliveryFailed || in.Delivery.Status() == DeliveryDone {
			e.finishIntent(in, now)
		}
	}
}

// channelRate is a channel's own rate= or notify.channel-rate.
func (e *Engine) channelRate(name string) float64 {
	if ch, ok := e.cfg.Channels[name]; ok && ch.Rate > 0 {
		return ch.Rate
	}
	return e.cfg.Tunables.Rate("notify.channel-rate")
}

func (e *Engine) channelBucket(name string) *ratelimit.Bucket {
	b := e.chanRate[name]
	if b == nil {
		r := e.channelRate(name)
		b = ratelimit.New(r, max(1, int(r*60)))
		e.chanRate[name] = b
	}
	return b
}

// serviceWindow is the margin over a minute in which a service's limit is
// counted, so a clock a little off the service's does not go over.
const serviceWindow = 62 * time.Second

// serviceLimit is what a channel's service takes in a minute
// (notify.service-limits), 0 when it is not limited.
func (e *Engine) serviceLimit(kind string) int {
	for _, kv := range strings.Split(e.cfg.Tunables.Raw("notify.service-limits"), ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && strings.EqualFold(strings.TrimSpace(k), kind) {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "/m")))
			return n
		}
	}
	return 0
}

// heldTooLong reports whether step i's channel is kept quiet by its
// service for longer than notify.retry-after-max while a later channel of
// a fallback chain could take the alert now.
func (e *Engine) heldTooLong(in *Intent, i int, name string, now time.Time) bool {
	until, held := e.chanHold[name]
	if !held || until.Sub(now) <= e.cfg.Tunables.Duration("notify.retry-after-max") || in.Delivery.Mode != DeliverFallback {
		return false
	}
	for _, s := range in.Delivery.Steps[i+1:] {
		if s.State == StepPending {
			return true
		}
	}
	return false
}

// channelAllows says whether a channel may send now: it is not held back
// by its service, the service's limit for a minute has room, and the
// channel's own rate allows it. What may not go stays queued, where a
// long queue becomes one summary instead of messages the service drops.
func (e *Engine) channelAllows(name, kind string, now time.Time) bool {
	if until, held := e.chanHold[name]; held {
		if now.Before(until) {
			return false
		}
		delete(e.chanHold, name)
	}
	if e.channelBucket(name).Delay(now) > 0 {
		return false
	}
	if lim := e.serviceLimit(kind); lim > 0 {
		w := e.chanWindow[name]
		if w == nil {
			w = &ratelimit.Window{Max: lim, Per: serviceWindow}
			e.chanWindow[name] = w
		}
		if !w.Allow(now) {
			return false
		}
	}
	return e.channelBucket(name).Allow(now)
}

// onAttempt records a finished channel attempt.
func (e *Engine) onAttempt(a attemptResult) {
	in := e.intents[a.intent]
	if in == nil || a.step >= len(in.Delivery.Steps) {
		return
	}
	now := e.now()
	name := in.Delivery.Steps[a.step].Channel
	retries := e.cfg.Tunables.Int("notify.channel-retries")
	if ch, ok := e.cfg.Channels[name]; ok && (ch.RetriesSet || ch.Retries > 0) {
		retries = ch.Retries
	}
	if !a.att.OK && (a.att.Permanent || permanentNotifyErr(a.att)) {
		retries = 0 // a wrong token or a refused message: on to the next channel
		e.log.Info("notify permanent error; not retrying channel", "channel", a.att.Channel, "status", a.att.Status, "err", a.att.Error)
	}
	backoff := notifyRetryBackoff(in.Delivery.Steps[a.step].Attempts, e.cfg.Tunables.Duration("notify.retry-backoff"))
	if wait := a.att.RetryAfter; !a.att.OK && wait > 0 {
		// The service is limiting this channel: nothing else goes through
		// it until then either.
		e.chanHold[name] = now.Add(wait)
		if wait <= e.cfg.Tunables.Duration("notify.retry-after-max") {
			backoff = wait
		} else {
			retries = 0 // too long to wait: the next channel takes the message
		}
	}
	in.Delivery.Finish(a.step, a.att.OK, a.att.Error, now, retries, backoff)
	in.UpdatedAt = now
	in.Attempts = append(in.Attempts, a.more...)
	if len(a.more) == 0 {
		in.Attempts = append(in.Attempts, a.att)
	}
	_ = e.deps.Sink.SaveIntent(in)
	if st := in.Delivery.Status(); st == DeliveryDone || st == DeliveryFailed {
		e.finishIntent(in, now)
	}
}

// finishIntent closes a delivered or failed intent.
func (e *Engine) finishIntent(in *Intent, now time.Time) {
	if _, ok := e.intents[in.ID]; !ok {
		return
	}
	delete(e.intents, in.ID)
	ok := in.Delivery.Status() == DeliveryDone
	in.State, in.UpdatedAt = IntentFailed, now
	if ok {
		in.State = IntentDone
	}
	_ = e.deps.Sink.SaveIntent(in)
	e.decideIntent(in, map[bool]string{true: LedSent, false: LedFailed}[ok], "")
	e.deps.Sink.Delivery(DeliveryRecord{
		IntentID: in.ID, IncidentID: in.IncidentID, Kind: in.Kind, Seq: in.Seq, At: now,
		Attempts: in.Attempts, OK: ok, Fallback: in.Delivery.Fallback(), Drill: in.Message.DryRun,
		Delayed: LateBy(in.DueAt, now, e.cfg.Tunables.Duration("notify.intent-late")),
	})
	sev := "info"
	if !ok {
		sev = "critical"
	}
	e.event(Event{Type: EventNotify, Severity: sev, Monitor: in.Message.Monitor, Message: in.Kind + " notification " + map[bool]string{true: "delivered", false: "failed"}[ok],
		Data: map[string]any{"msg": map[bool]string{true: "notify_delivered", false: "notify_failed"}[ok], "kind": in.Kind, "intent_id": in.ID, "incident_id": in.IncidentID, "fallback": in.Delivery.Fallback()}})
	if ok {
		e.credit(in)
		if inc := e.system[SystemAllFailed]; inc != nil && inc.State.Open() && !in.System {
			e.resolveSystem(SystemAllFailed, now, "")
		}
		return
	}
	if !in.System {
		e.openSystem(SystemAllFailed, now, e.text("all_failed", in.Policy, in.Message.Title))
		if e.notDelivered(in, now) {
			e.tellOnFail(in, now)
		}
	} else if in.Kind == "SYSTEM" && !in.Message.DryRun {
		// Most likely this host's own network: said in its summary.
		e.unsent = append(e.unsent[max(0, len(e.unsent)-9):], unsentNote{at: in.CreatedAt, text: firstLine(cmp.Or(in.Message.Body, in.Message.Title))})
	}
}

// notDelivered books a problem notification that no channel delivered on
// its incident, which sends it again after notify.failed-retry. It
// reports whether this is the first failure in a row: the system channels
// are told once, not at every try.
func (e *Engine) notDelivered(in *Intent, now time.Time) bool {
	switch NoticeKind(in.Kind) {
	case NoticeProblem, NoticeRepeat, NoticeReopen:
	default:
		return true
	}
	inc := e.incidents[in.IncidentID]
	if inc == nil || in.Message.DryRun || !inc.State.Open() {
		return true
	}
	wait := e.cfg.Tunables.Duration("notify.failed-retry")
	if wait <= 0 {
		return true // off: the repeat interval decides, as before
	}
	inc.State.NotDelivered(now, wait, e.effectivePolicy(inc, e.policy(inc.Policy)).Repeat)
	e.saveIncident(inc)
	return inc.State.Undelivered == 1
}

// tellOnFail sends a notice that could not be delivered to each of the
// policy's OnFail chains (a team's: the system channels, and the team's
// administrators), saying so.
func (e *Engine) tellOnFail(in *Intent, now time.Time) {
	pol := e.policy(in.Policy)
	if len(pol.OnFail) == 0 || in.Message.DryRun || len(in.Chain) > 0 {
		return
	}
	msg := in.Message
	msg.Body = strings.TrimSpace(e.text("all_failed", in.Policy, in.Message.Title) + "\n" + msg.Body)
	for _, chain := range pol.OnFail {
		tell := &Intent{IncidentID: in.IncidentID, Seq: in.Seq, Kind: in.Kind, Policy: in.Policy, Message: msg,
			CreatedAt: now, DueAt: now, System: true, Chain: chain}
		if e.enqueue(tell) {
			e.decideIntent(tell, LedQueued, "team channels failed")
		}
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// unsentNote is a system notice that could not be sent.
type unsentNote struct {
	at   time.Time
	text string
}

// takeUnsent returns, once, the system notices that could not be sent
// since a time, as one line.
func (e *Engine) takeUnsent(since time.Time) string {
	var items []string
	for _, u := range e.unsent {
		if !u.at.Before(since) && !slices.Contains(items, u.text) {
			items = append(items, u.text)
		}
	}
	e.unsent = nil
	return strings.Join(items, "; ")
}

// digest queues one summary notice standing for the incidents ids; chain
// replaces the policy's channels (all of them notified) when set.
func (e *Engine) digest(pol string, chain []string, title, body string, ids []int64, now time.Time) {
	d := &Intent{Kind: "DIGEST", Policy: pol, CreatedAt: now, DueAt: now, Chain: chain, Covers: ids,
		Message: notify.Message{Kind: "DIGEST", Priority: "high", Title: title, Body: body, Time: now, Instance: e.cfg.Instance, Lang: e.cfg.Lang}}
	e.enqueue(d)
}

// credit books a delivered problem notice on its incident (or on every
// incident a digest stood for), so a recovery is owed only to people who
// actually heard about the problem.
func (e *Engine) credit(in *Intent) {
	ids := append([]int64(nil), in.Covers...)
	switch NoticeKind(in.Kind) {
	case NoticeProblem, NoticeRepeat, NoticeReopen, NoticeEscalate:
		ids = append(ids, in.IncidentID)
	}
	for _, id := range ids {
		if inc := e.incidents[id]; inc != nil {
			inc.State.Delivered()
			e.saveIncident(inc)
		}
	}
}

// coalesce applies storm control: when a policy's queue of undelivered
// notices grows past notify.coalesce-queue, or its oldest has waited longer
// than notify.coalesce-wait, the queued notices are replaced by one digest.
func (e *Engine) coalesce(now time.Time) {
	if len(e.localnet.Active()) > 0 {
		// Notices held by a local outage are judged when it ends, not merged.
		return
	}
	maxQ := e.cfg.Tunables.Int("notify.coalesce-queue")
	maxWait := e.cfg.Tunables.Duration("notify.coalesce-wait")
	byPolicy := map[string][]*Intent{}
	for _, in := range e.pendingIntents() {
		if in.System || in.Kind == "DIGEST" || in.Delivery.Pos() > 0 || in.Delivery.Steps[0].Attempts > 0 {
			continue // only untouched, mergeable notices
		}
		byPolicy[in.Policy] = append(byPolicy[in.Policy], in)
	}
	for pol, q := range byPolicy {
		if len(q) <= maxQ && now.Sub(q[0].CreatedAt) <= maxWait {
			continue
		}
		for _, in := range q {
			e.saved(CounterOutageMerged, in.Message.Monitor, 1)
			in.State, in.UpdatedAt = IntentMerged, now
			_ = e.deps.Sink.SaveIntent(in)
			delete(e.intents, in.ID)
			e.decideIntent(in, LedMerged, "storm control: merged into a summary")
		}
		// Services first and urgent; network causes said as such, to the
		// network team when the policy names one (network=).
		service, network := e.classify(q)
		var netIDs []int64
		for _, p := range network {
			netIDs = append(netIDs, p.ids...)
		}
		netTitle, netBody := e.text("digest_network_title", partsLen(network)), e.networkBody(network)
		svcTitle := e.text("digest_service_title", len(service.lines))
		svcBody := e.text("digest_body", service.lines, e.cfg.BaseURL)
		switch {
		case len(network) == 0:
			e.digest(pol, nil, svcTitle, svcBody, service.ids, now)
		case len(e.policy(pol).Network) > 0:
			if len(service.lines) > 0 {
				e.digest(pol, nil, svcTitle, svcBody, service.ids, now)
			}
			e.digest(pol, e.policy(pol).Network, netTitle, netBody, netIDs, now)
		case len(service.lines) > 0:
			e.digest(pol, nil, svcTitle, svcBody+"\n\n"+netBody, append(service.ids, netIDs...), now)
		default:
			e.digest(pol, nil, netTitle, netBody, netIDs, now)
		}
	}
}
