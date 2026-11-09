package engine

import (
	"reflect"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/tunables"
)

func defaultPolicy() NotifyPolicy { return DefaultNotifyPolicy(tunables.Set{}) }

func has(events []string, e string) bool {
	for _, x := range events {
		if x == e {
			return true
		}
	}
	return false
}

func TestDefaultNotifyPolicy(t *testing.T) {
	p := defaultPolicy()
	if p.Max != 3 || p.Repeat != 30*time.Minute || p.ReopenWindow != 30*time.Minute || p.Delay != 0 || !p.Recovery || p.NotifyDegraded || p.DegradedMax != 1 || !p.AckClearOnChange || !p.RecoveryAfterAck {
		t.Fatalf("%+v", p)
	}
	s, _ := tunables.New(map[string]string{"notify.max": "5", "notify.repeat": "10m"})
	if p := DefaultNotifyPolicy(s); p.Max != 5 || p.Repeat != 10*time.Minute {
		t.Fatalf("tunable overrides: %+v", p)
	}
}

// The capped sequence: first notice at once, repeats every 30m, stop at 3,
// one recovery notice with the count.
func TestCappedSequence(t *testing.T) {
	p := defaultPolicy()
	s, ns, _ := Fail(nil, t0, "network", p)
	if !reflect.DeepEqual(ns, []PlannedNotice{{Kind: NoticeProblem, Seq: 1, Max: 3}}) {
		t.Fatalf("first: %+v", ns)
	}
	s.Delivered()
	if ns, _ := Advance(s, t0.Add(29*time.Minute), p); len(ns) != 0 {
		t.Fatalf("early repeat: %+v", ns)
	}
	ns, _ = Advance(s, t0.Add(30*time.Minute), p)
	if len(ns) != 1 || ns[0].Kind != NoticeRepeat || ns[0].Seq != 2 {
		t.Fatalf("repeat 2: %+v", ns)
	}
	ns, ev := Advance(s, t0.Add(60*time.Minute), p)
	if len(ns) != 1 || ns[0].Seq != 3 || !s.Capped || !has(ev, IncidentCapped) {
		t.Fatalf("repeat 3 + capped: %+v %v %+v", ns, ev, s)
	}
	for m := 90; m < 600; m += 30 {
		if ns, _ := Advance(s, t0.Add(time.Duration(m)*time.Minute), p); len(ns) != 0 {
			t.Fatalf("after cap at %dm: %+v", m, ns)
		}
	}
	ns = Recover(s, t0.Add(10*time.Hour), p)
	if !reflect.DeepEqual(ns, []PlannedNotice{{Kind: NoticeRecovery, Seq: 3, Max: 3}}) {
		t.Fatalf("recovery: %+v", ns)
	}
	_, ev = Advance(s, t0.Add(10*time.Hour+30*time.Minute), p)
	if !s.Closed || !has(ev, IncidentClosed) {
		t.Fatalf("close after the reopen window: %+v %v", s, ev)
	}
}

func TestUnlimitedMax(t *testing.T) {
	p := defaultPolicy()
	p.Max = 0
	s, _, _ := Fail(nil, t0, "", p)
	for i := 1; i <= 10; i++ {
		ns, _ := Advance(s, t0.Add(time.Duration(i)*p.Repeat), p)
		if len(ns) != 1 || ns[0].Seq != i+1 || ns[0].Max != 0 {
			t.Fatalf("repeat %d: %+v", i, ns)
		}
	}
	if s.Capped {
		t.Fatal("unlimited never caps")
	}
}

func TestDelayAndRecoveryWithinDelay(t *testing.T) {
	p := defaultPolicy()
	p.Delay = 2 * time.Minute
	s, ns, _ := Fail(nil, t0, "", p)
	if len(ns) != 0 {
		t.Fatalf("delayed: %+v", ns)
	}
	if ns, _ := Advance(s, t0.Add(time.Minute), p); len(ns) != 0 {
		t.Fatalf("within delay: %+v", ns)
	}
	// Recovered within the delay: nobody hears anything, not even a recovery.
	if ns := Recover(s, t0.Add(90*time.Second), p); len(ns) != 0 {
		t.Fatalf("recovery after silent incident: %+v", ns)
	}
	if ns, _ := Advance(s, t0.Add(3*time.Minute), p); len(ns) != 0 {
		t.Fatalf("delayed notice after recovery: %+v", ns)
	}
	// Failing again within the window restarts the delay.
	s, ns, ev := Fail(s, t0.Add(5*time.Minute), "", p)
	if len(ns) != 0 || !has(ev, IncidentReopened) {
		t.Fatalf("reopen restarts delay: %+v %v", ns, ev)
	}
	ns, _ = Advance(s, t0.Add(7*time.Minute), p)
	if len(ns) != 1 || ns[0].Kind != NoticeProblem || ns[0].Seq != 1 {
		t.Fatalf("first notice after delay: %+v", ns)
	}
}

func TestReopenCountsAgainstCap(t *testing.T) {
	p := defaultPolicy()
	s, _, _ := Fail(nil, t0, "", p)
	Recover(s, t0.Add(5*time.Minute), p)
	s2, ns, ev := Fail(s, t0.Add(10*time.Minute), "", p)
	if s2 != s || !has(ev, IncidentReopened) || s.ReopenCount != 1 {
		t.Fatalf("reopen same incident: %+v %v", s, ev)
	}
	if !reflect.DeepEqual(ns, []PlannedNotice{{Kind: NoticeReopen, Seq: 2, Max: 3}}) {
		t.Fatalf("reopen notice: %+v", ns)
	}
	// Back for more than half the window: the count starts again.
	s.Delivered()
	Recover(s, t0.Add(11*time.Minute), p)
	if _, ns, _ = Fail(s, t0.Add(11*time.Minute+p.ReopenWindow/2), "", p); len(ns) != 1 || ns[0].Kind != NoticeReopen || ns[0].Seq != 1 {
		t.Fatalf("reopen after a real recovery: %+v", ns)
	}
	// Outside the window: a brand-new incident.
	Recover(s, t0.Add(30*time.Minute), p)
	s3, ns, _ := Fail(s, t0.Add(30*time.Minute+p.ReopenWindow), "", p)
	if s3 == s || len(ns) != 1 || ns[0].Kind != NoticeProblem || ns[0].Seq != 1 {
		t.Fatalf("new incident outside window: %+v", ns)
	}
}

// Flapping after the cap: intermediate recoveries and failures are quiet;
// one summary once it stayed fine for the whole window.
func TestFlappingWhileCapped(t *testing.T) {
	p := defaultPolicy()
	p.Max = 1
	s, ns, _ := Fail(nil, t0, "", p)
	s.Delivered()
	if len(ns) != 1 || !s.Capped {
		t.Fatalf("capped at once: %+v", s)
	}
	// First recovery after the cap is still reported (no flaps yet).
	if ns := Recover(s, t0.Add(10*time.Minute), p); len(ns) != 1 || ns[0].Kind != NoticeRecovery {
		t.Fatalf("recovery: %+v", ns)
	}
	at := t0.Add(10 * time.Minute)
	for i := 0; i < 4; i++ {
		at = at.Add(5 * time.Minute)
		if _, ns, _ := Fail(s, at, "", p); len(ns) != 0 {
			t.Fatalf("flap %d notified: %+v", i, ns)
		}
		at = at.Add(time.Minute)
		if ns := Recover(s, at, p); len(ns) != 0 {
			t.Fatalf("flap recovery %d notified: %+v", i, ns)
		}
	}
	if ns, _ := Advance(s, at.Add(p.ReopenWindow-time.Second), p); len(ns) != 0 {
		t.Fatalf("summary too early: %+v", ns)
	}
	ns, ev := Advance(s, at.Add(p.ReopenWindow), p)
	if !reflect.DeepEqual(ns, []PlannedNotice{{Kind: NoticeRecovery, Seq: 1, Max: 1, Flaps: 4}}) || !has(ev, IncidentClosed) {
		t.Fatalf("summary: %+v %v", ns, ev)
	}
}

func TestAckStopsRepeatsAndExpires(t *testing.T) {
	p := defaultPolicy()
	s, _, _ := Fail(nil, t0, "network", p)
	Ack(s, "alice", t0.Add(time.Minute), time.Hour, p)
	if ns, _ := Advance(s, t0.Add(45*time.Minute), p); len(ns) != 0 {
		t.Fatalf("acked repeat: %+v", ns)
	}
	ns, ev := Advance(s, t0.Add(61*time.Minute), p)
	if !has(ev, IncidentAckExpired) || len(ns) != 1 || ns[0].Kind != NoticeRepeat {
		t.Fatalf("ack expiry resumes repeats: %+v %v", ns, ev)
	}
}

func TestAckPolicyExpiryAndRecoveryAfterAck(t *testing.T) {
	p := defaultPolicy()
	p.AckExpire = 4 * time.Hour
	p.RecoveryAfterAck = false
	s, _, _ := Fail(nil, t0, "", p)
	Ack(s, "bob", t0, 0, p)
	if !s.AckExpiresAt.Equal(t0.Add(4 * time.Hour)) {
		t.Fatalf("policy ack-expire: %v", s.AckExpiresAt)
	}
	if ns := Recover(s, t0.Add(time.Hour), p); len(ns) != 0 {
		t.Fatalf("recovery-after-ack=no: %+v", ns)
	}
}

func TestAckClearedOnReasonChange(t *testing.T) {
	p := defaultPolicy()
	s, _, _ := Fail(nil, t0, "network", p)
	Ack(s, "alice", t0, 0, p)
	if ReasonChanged(s, "network", p) || !s.Acked {
		t.Fatal("same class keeps the ACK")
	}
	if !ReasonChanged(s, "protocol", p) || s.Acked {
		t.Fatal("class change clears the ACK")
	}
	p.AckClearOnChange = false
	Ack(s, "alice", t0, 0, p)
	if ReasonChanged(s, "network", p) || !s.Acked {
		t.Fatal("ack-clear-on-change=no keeps the ACK")
	}
}

func TestAckedFlapsFoldIntoSummary(t *testing.T) {
	p := defaultPolicy()
	s, _, _ := Fail(nil, t0, "", p)
	s.Delivered()
	Ack(s, "alice", t0, 0, p)
	Recover(s, t0.Add(time.Minute), p)
	if _, ns, _ := Fail(s, t0.Add(2*time.Minute), "", p); len(ns) != 0 {
		t.Fatalf("acked reopen notified: %+v", ns)
	}
	if ns := Recover(s, t0.Add(3*time.Minute), p); len(ns) != 0 {
		t.Fatalf("acked flap recovery: %+v", ns)
	}
	ns, _ := Advance(s, t0.Add(3*time.Minute+p.ReopenWindow), p)
	if len(ns) != 1 || ns[0].Flaps != 1 {
		t.Fatalf("summary: %+v", ns)
	}
}

func TestSilence(t *testing.T) {
	p := defaultPolicy()
	s, _, _ := Fail(nil, t0, "", p)
	Silence(s, t0.Add(4*time.Hour))
	for m := 30; m < 240; m += 30 {
		if ns, _ := Advance(s, t0.Add(time.Duration(m)*time.Minute), p); len(ns) != 0 {
			t.Fatalf("silenced at %dm: %+v", m, ns)
		}
	}
	ns, ev := Advance(s, t0.Add(4*time.Hour), p)
	if !has(ev, IncidentSilenceEnded) || len(ns) != 1 || ns[0].Seq != 2 {
		t.Fatalf("silence end: %+v %v", ns, ev)
	}
}

func TestLowPriorityIncident(t *testing.T) {
	p := defaultPolicy()
	p.NotifyDegraded = true
	s := &IncidentState{Low: true, OpenedAt: t0, HardAt: t0}
	ns, _ := Advance(s, t0, p)
	if len(ns) != 1 || ns[0].Max != 1 || !s.Capped {
		t.Fatalf("low first notice: %+v", ns)
	}
	if ns, _ := Advance(s, t0.Add(time.Hour), p); len(ns) != 0 {
		t.Fatalf("low repeats: %+v", ns)
	}
	if ns := Recover(s, t0.Add(2*time.Hour), p); len(ns) != 0 {
		t.Fatalf("low recovery is silent: %+v", ns)
	}
	p.NotifyDegraded = false
	s = &IncidentState{Low: true, OpenedAt: t0, HardAt: t0}
	if ns, _ := Advance(s, t0, p); len(ns) != 0 {
		t.Fatalf("notify-degraded=no: %+v", ns)
	}
}

func TestRecoveryDisabled(t *testing.T) {
	p := defaultPolicy()
	p.Recovery = false
	s, _, _ := Fail(nil, t0, "", p)
	if ns := Recover(s, t0.Add(time.Minute), p); len(ns) != 0 {
		t.Fatalf("recovery=no: %+v", ns)
	}
	if ns := Recover(s, t0.Add(2*time.Minute), p); ns != nil {
		t.Fatal("recovering twice")
	}
}

func TestNextDaily(t *testing.T) {
	sh := time.FixedZone("CST", 8*3600)
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 10, 1, 9, 59, 0, 0, sh), time.Date(2026, 10, 1, 10, 0, 0, 0, sh)},
		{time.Date(2026, 10, 1, 10, 0, 0, 0, sh), time.Date(2026, 10, 2, 10, 0, 0, 0, sh)},
		{time.Date(2026, 12, 31, 23, 0, 0, 0, sh), time.Date(2027, 1, 1, 10, 0, 0, 0, sh)},
		// UTC input, local zone decides the day.
		{time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 10, 0, 0, 0, sh)},
	}
	for _, c := range cases {
		if got := NextDaily(c.now, 10, 0, sh); !got.Equal(c.want) {
			t.Errorf("NextDaily(%v)=%v want %v", c.now, got, c.want)
		}
	}
	// A DST change must not skip or repeat a day.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	got := NextDaily(time.Date(2026, 3, 7, 12, 0, 0, 0, ny), 10, 0, ny)
	if want := time.Date(2026, 3, 8, 10, 0, 0, 0, ny); !got.Equal(want) {
		t.Errorf("DST: %v want %v", got, want)
	}
}

// A problem notice that was planned but never delivered (queued during a
// local outage, then dropped) owes nobody a recovery.
func TestRecoveryOnlyAfterDelivery(t *testing.T) {
	p := defaultPolicy()
	s, ns, _ := Fail(nil, t0, "network", p)
	if len(ns) != 1 || s.NotifyCount != 1 {
		t.Fatalf("problem: %+v", ns)
	}
	if ns := Recover(s, t0.Add(time.Minute), p); len(ns) != 0 {
		t.Fatalf("recovery for a problem nobody received: %+v", ns)
	}
	s, _, _ = Fail(nil, t0, "network", p)
	s.Delivered()
	if ns := Recover(s, t0.Add(time.Minute), p); len(ns) != 1 || ns[0].Kind != NoticeRecovery {
		t.Fatalf("recovery after a delivered problem: %+v", ns)
	}
	// Incidents from before delivery was counted keep the old rule.
	old := &IncidentState{OpenedAt: t0, HardAt: t0, NotifyCount: 1}
	if ns := Recover(old, t0.Add(time.Minute), p); len(ns) != 1 {
		t.Fatalf("recovery of an older incident: %+v", ns)
	}
}
