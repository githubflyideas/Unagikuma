package engine

import (
	"cmp"
	"context"
	"slices"
	"sort"
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
			if !e.channelBucket(name).Allow(now) {
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
			members := e.rotationMembers(ch, in, now)
			wg.Add(1)
			go func() {
				defer wg.Done()
				sctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				res := attemptResult{intent: id, step: step}
				switch {
				case len(ch.Members) == 0:
					res.att = e.deps.Send(sctx, ch.Channel, &msg)
				case ch.Rotation != nil:
					res.att, res.more = e.sendRotation(sctx, members, &msg)
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

func (e *Engine) channelBucket(name string) *ratelimit.Bucket {
	b := e.chanRate[name]
	if b == nil {
		r := e.cfg.Tunables.Rate("notify.channel-rate")
		b = ratelimit.New(r, max(1, int(r*60)))
		e.chanRate[name] = b
	}
	return b
}

// onAttempt records a finished channel attempt.
func (e *Engine) onAttempt(a attemptResult) {
	in := e.intents[a.intent]
	if in == nil || a.step >= len(in.Delivery.Steps) {
		return
	}
	now := e.now()
	retries := e.cfg.Tunables.Int("notify.channel-retries")
	if ch, ok := e.cfg.Channels[in.Delivery.Steps[a.step].Channel]; ok && ch.Retries > 0 {
		retries = ch.Retries
	}
	if !a.att.OK && permanentNotifyErr(a.att) {
		retries = 0 // auth / bad request: skip to fallback channel
		e.log.Info("notify permanent error; not retrying channel", "channel", a.att.Channel, "status", a.att.Status, "err", a.att.Error)
	}
	backoff := notifyRetryBackoff(in.Delivery.Steps[a.step].Attempts, e.cfg.Tunables.Duration("notify.retry-backoff"))
	if !a.att.OK && a.att.Status == 429 {
		if d := retryAfterDuration(a.att.Response); d > 0 && d < 60*time.Second {
			backoff = d
		}
	}
	in.Delivery.Finish(a.step, a.att.OK, a.att.Error, now, retries, backoff)
	if a.att.OK {
		e.noteResponder(in, in.Delivery.Steps[a.step].Channel, a.more)
	}
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
		e.tellOnFail(in, now)
	} else if in.Kind == "SYSTEM" && !in.Message.DryRun {
		// Most likely this host's own network: said in its summary.
		e.unsent = append(e.unsent[max(0, len(e.unsent)-9):], unsentNote{at: in.CreatedAt, text: firstLine(cmp.Or(in.Message.Body, in.Message.Title))})
	}
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
			in.State, in.UpdatedAt = IntentMerged, now
			_ = e.deps.Sink.SaveIntent(in)
			delete(e.intents, in.ID)
			e.decideIntent(in, LedMerged, "storm control: merged into a summary")
		}
		e.deps.Sink.Counter(CounterOutageMerged, len(q))
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
