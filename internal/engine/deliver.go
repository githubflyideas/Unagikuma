package engine

import (
	"time"
)

// Delivery walks a policy's channel chain for one notification intent.
//
// Mode "fallback" (default) tries the channels in order and stops at the
// first success; mode "all" sends to every channel. Each step's state is
// persisted with the intent (as JSON) before and after every attempt, so a
// restart continues where it stopped instead of going back to the head of
// the chain. A step found "sending" after a crash is attempted again: the
// receiver may see that message twice, with the same delivery id.
//
// While this host's network is out, steps whose channel cannot be reached
// (a wan webhook during an uplink outage) are paused rather than failed:
// the intent waits for them, while channels that still work (an SMTP relay
// on the LAN) are used right away.

// Delivery modes.
const (
	DeliverFallback = "fallback"
	DeliverAll      = "all"
)

// StepState is the state of one chain step.
type StepState string

const (
	StepPending StepState = ""
	StepSending StepState = "sending"
	StepOK      StepState = "ok"
	StepFailed  StepState = "failed"
	// StepSkipped: the channel is outside its time period; the chain goes
	// on as if it were not there.
	StepSkipped StepState = "skipped"
)

// ChainStep is one channel of the chain.
type ChainStep struct {
	Channel  string    `json:"ch"`
	State    StepState `json:"st,omitempty"`
	Attempts int       `json:"n,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitempty"`
	Error    string    `json:"err,omitempty"`
}

// Delivery is the persisted chain state of one intent.
type Delivery struct {
	Mode  string      `json:"mode,omitempty"`
	Steps []ChainStep `json:"steps"`
}

// DeliveryStatus summarises a delivery.
type DeliveryStatus int

const (
	// DeliveryActive: a step can be attempted now or is in flight.
	DeliveryActive DeliveryStatus = iota
	// DeliveryWaiting: nothing to do until a retry is due or a paused
	// channel becomes reachable again.
	DeliveryWaiting
	// DeliveryDone: delivered (fallback: one success; all: every step
	// finished and at least one succeeded).
	DeliveryDone
	// DeliveryFailed: every step failed — the system raises ALL_FAILED.
	DeliveryFailed
)

// NewDelivery starts a delivery over the given channel names.
func NewDelivery(chain []string, mode string) Delivery {
	if mode != DeliverAll {
		mode = DeliverFallback
	}
	d := Delivery{Mode: mode, Steps: make([]ChainStep, len(chain))}
	for i, ch := range chain {
		d.Steps[i].Channel = ch
	}
	return d
}

func (s ChainStep) terminal() bool {
	return s.State == StepOK || s.State == StepFailed || s.State == StepSkipped
}

// Skip marks step i as outside its channel's time period.
func (d *Delivery) Skip(i int) {
	d.Steps[i].State, d.Steps[i].Error = StepSkipped, "outside its time period"
}

// Recover turns steps left "sending" by a crash back into pending ones.
func (d *Delivery) Recover() {
	for i := range d.Steps {
		if d.Steps[i].State == StepSending {
			d.Steps[i].State = StepPending
		}
	}
}

// Next returns the steps to attempt now. paused reports channels that
// cannot be reached at the moment (nil = none).
func (d *Delivery) Next(now time.Time, paused func(channel string) bool) ([]int, DeliveryStatus) {
	if st := d.Status(); st == DeliveryDone || st == DeliveryFailed {
		return nil, st
	}
	ready := func(s ChainStep) bool {
		return s.State == StepPending && !now.Before(s.RetryAt) && (paused == nil || !paused(s.Channel))
	}
	var out []int
	for i, s := range d.Steps {
		if s.State == StepSending {
			if d.Mode == DeliverFallback {
				return nil, DeliveryActive // one attempt at a time
			}
			continue
		}
		if ready(s) {
			out = append(out, i)
			if d.Mode == DeliverFallback {
				break
			}
			continue
		}
		if d.Mode == DeliverFallback && s.State == StepPending && now.Before(s.RetryAt) && (paused == nil || !paused(s.Channel)) {
			// Retry the same channel after its backoff before falling back.
			break
		}
	}
	if len(out) > 0 {
		return out, DeliveryActive
	}
	for _, s := range d.Steps {
		if s.State == StepSending {
			return nil, DeliveryActive
		}
	}
	return nil, DeliveryWaiting
}

// Begin marks step i as being attempted.
func (d *Delivery) Begin(i int) {
	d.Steps[i].State = StepSending
	d.Steps[i].Attempts++
}

// Finish records the outcome of step i. A failed step is retried up to
// retries more times, each after backoff, before it is given up.
func (d *Delivery) Finish(i int, ok bool, errMsg string, now time.Time, retries int, backoff time.Duration) {
	s := &d.Steps[i]
	switch {
	case ok:
		s.State, s.Error, s.RetryAt = StepOK, "", time.Time{}
	case s.Attempts <= retries:
		s.State, s.Error, s.RetryAt = StepPending, errMsg, now.Add(backoff)
	default:
		s.State, s.Error, s.RetryAt = StepFailed, errMsg, time.Time{}
	}
}

// Status reports the overall state without considering pauses. Skipped
// steps count neither way: a delivery whose channels are all outside their
// periods is done, one with failures and no success has failed.
func (d *Delivery) Status() DeliveryStatus {
	okN, failN, skipN := 0, 0, 0
	for _, s := range d.Steps {
		switch s.State {
		case StepOK:
			okN++
		case StepFailed:
			failN++
		case StepSkipped:
			skipN++
		}
	}
	switch {
	case len(d.Steps) == 0:
		return DeliveryFailed
	case d.Mode == DeliverFallback && okN > 0:
		return DeliveryDone
	case okN+failN+skipN < len(d.Steps):
		return DeliveryActive
	case okN == 0 && failN > 0:
		return DeliveryFailed
	}
	return DeliveryDone
}

// Fallback reports whether delivery succeeded only after an earlier
// channel failed or was skipped (shown as "fallback" in the log).
func (d *Delivery) Fallback() bool {
	if d.Mode != DeliverFallback {
		return false
	}
	for i, s := range d.Steps {
		if s.State == StepOK {
			return i > 0 // every earlier step failed or was paused
		}
	}
	return false
}

// Channels lists the channels of the chain, in order.
func (d *Delivery) Channels() []string {
	out := make([]string, len(d.Steps))
	for i, s := range d.Steps {
		out[i] = s.Channel
	}
	return out
}

// Pos is the chain position persisted as chain_pos: the first step that is
// not finished (len(Steps) when all are).
func (d *Delivery) Pos() int {
	for i, s := range d.Steps {
		if !s.terminal() {
			return i
		}
	}
	return len(d.Steps)
}

// LateBy returns how late a delivery is relative to when it was due, when
// that exceeds the notify.intent-late threshold; the message is then tagged
// "[delayed Xm]". Zero otherwise.
func LateBy(due, now time.Time, threshold time.Duration) time.Duration {
	if late := now.Sub(due); late > threshold {
		return late
	}
	return 0
}
