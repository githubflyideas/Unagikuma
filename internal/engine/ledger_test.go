package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// ledgerSink keeps the decisions.
type ledgerSink struct {
	*memSink
	ds []Decision
}

func (s *ledgerSink) Decision(d Decision) { s.ds = append(s.ds, d) }

func ledgerEngine(t *testing.T, kv map[string]string) (*Engine, *ledgerSink) {
	t.Helper()
	sink := &ledgerSink{memSink: newMemSink()}
	e := New(Deps{Sink: sink})
	ts, err := tunables.New(kv)
	if err != nil {
		t.Fatal(err)
	}
	pol := PolicySpec{Name: "default", Chain: []string{"ops"}, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}
	e.cfg = Config{Tunables: ts, Location: time.UTC, Policies: map[string]PolicySpec{"default": pol},
		Channels: map[string]ChannelSpec{"ops": {Channel: notify.Channel{Name: "ops", Kind: "log"}}}}
	return e, sink
}

// Each failure is told as what it is: a service answering wrong, a whole
// segment failing at the network level, or a target that cannot be
// reached (it or the path).
func TestClassOf(t *testing.T) {
	e, _ := ledgerEngine(t, nil)
	set := func(name, reason, domain string) {
		e.states[name] = &monRuntime{LastReason: reason, Domain: domain, State: MonState{Avail: StatusCritical}}
	}
	set("shop", probe.ReasonHTTPStatus, "4:203.0.113.0/24")
	set("api", probe.ReasonTimeout, "4:198.51.100.0/24")
	for _, n := range []string{"db1", "db2", "db3"} {
		set(n, probe.ReasonTimeout, "4:10.0.3.0/24")
	}
	cases := map[string]string{"shop": ClassBusiness, "api": ClassUncertain, "db1": ClassNetwork}
	for name, want := range cases {
		class, ev := e.classOf(&Incident{Monitor: name, Reason: e.states[name].LastReason})
		if class != want {
			t.Errorf("%s: %s, want %s (%v)", name, class, want, ev)
		}
		line := e.classLine(class, NoticeProblem, ev)
		if line == "" {
			t.Errorf("%s: no class line", name)
		}
		if name == "db1" && (!strings.Contains(line, "10.0.3.0/24") || !strings.Contains(line, "3 monitors")) {
			t.Errorf("segment line %q", line)
		}
	}
	if class, _ := e.classOf(&Incident{Kind: IncidentSystem, System: SystemAllFailed}); class != ClassSystem {
		t.Errorf("system incident: %s", class)
	}
	if e.classLine(ClassBusiness, NoticeRecovery, nil) != "" {
		t.Error("a recovery carries no class line")
	}
}

// No channel gets more than three alerts a minute: the rest wait for one
// digest that names how many, the monitors and the worst. Escalations
// and recoveries always go through, and new incidents up to their own share.
func TestBackstopFoldsIntoDigest(t *testing.T) {
	e, sink := ledgerEngine(t, nil)
	now := t0
	for range 3 {
		e.noteSent("ops", now)
	}
	mk := func(id int64, kind NoticeKind, seq int, mon string) *Intent {
		in := &Intent{IncidentID: id, Seq: seq, Kind: string(kind), Policy: "default", CreatedAt: now, DueAt: now,
			Message: notify.Message{Kind: string(kind), Monitor: mon, Title: mon, Priority: "high", Reason: probe.ReasonTimeout}}
		e.enqueue(in)
		return in
	}
	fresh := mk(1, NoticeProblem, 1, "web")
	esc := mk(2, NoticeEscalate, 0, "db")
	rep := mk(3, NoticeRepeat, 2, "api")
	rec := mk(4, NoticeRecovery, 1, "mail")
	for _, in := range []*Intent{fresh, esc, rec} {
		if e.backstop(in, now) {
			t.Errorf("%s must go through", in.Kind)
		}
	}
	for _, in := range []*Intent{rep} {
		if !e.backstop(in, now) {
			t.Errorf("%s should be folded", in.Kind)
		}
	}
	if e.intents[rep.ID] != nil || len(e.folded["default"].ins) != 1 {
		t.Fatal("folded alerts leave the queue")
	}
	e.flushBackstop(now.Add(30 * time.Second))
	if len(e.folded) != 1 {
		t.Fatal("the digest waits for the window")
	}
	e.flushBackstop(now.Add(61 * time.Second))
	var dg *Intent
	for _, in := range e.intents {
		if in.Kind == "DIGEST" {
			dg = in
		}
	}
	if dg == nil {
		t.Fatal("no digest")
	}
	b := dg.Message.Body
	// A recovery is never folded: it tells people they can stop.
	if !strings.Contains(dg.Message.Title, "1") || !strings.Contains(b, "Total alerts: 1") || !strings.Contains(b, "api") || strings.Contains(b, "mail") {
		t.Errorf("digest %q / %q", dg.Message.Title, b)
	}
	if len(dg.Covers) != 1 || dg.Covers[0] != 3 {
		t.Errorf("the digest stands for the repeated problem: %v", dg.Covers)
	}
	if !backstopExempt(dg) {
		t.Error("a digest is never folded")
	}
	folded := 0
	for _, d := range sink.ds {
		if d.Step == LedBackstop {
			folded++
		}
	}
	if folded != 1 {
		t.Errorf("ledger: %d backstop entries", folded)
	}
	// New incidents have a share of their own: once three went out this
	// window, a flood of them is folded too.
	for range 3 {
		e.noteSent("new:ops", now)
	}
	if flood := mk(5, NoticeProblem, 1, "cdn"); !e.backstop(flood, now) {
		t.Error("a fourth new problem in a minute should be folded")
	}
	// The window moves on.
	if e.overBackstop("ops", now.Add(61*time.Second)) {
		t.Error("a new window starts empty")
	}
	// Off.
	e2, _ := ledgerEngine(t, map[string]string{"notify.backstop-max": "0"})
	for range 10 {
		e2.noteSent("ops", now)
	}
	if e2.overBackstop("ops", now) {
		t.Error("backstop-max=0 turns it off")
	}
}

// Every step of an alert lands in the ledger: queued with its class and
// evidence, then sent with its receipts.
func TestLedgerRecordsSteps(t *testing.T) {
	e, sink := ledgerEngine(t, nil)
	e.states["shop"] = &monRuntime{LastReason: probe.ReasonHTTPStatus, Domain: "4:203.0.113.0/24", State: MonState{Avail: StatusCritical}}
	inc := &Incident{ID: 7, Monitor: "shop", Reason: probe.ReasonHTTPStatus, Policy: "default", State: IncidentState{HardAt: t0}}
	e.incidents[7] = inc
	class, ev := e.classOf(inc)
	in := &Intent{IncidentID: 7, Seq: 1, Kind: "PROBLEM", Policy: "default", CreatedAt: t0, DueAt: t0, Evidence: ev,
		Message: notify.Message{Kind: "PROBLEM", Monitor: "shop", Class: class}}
	e.enqueue(in)
	e.decideIntent(in, LedQueued, "")
	in.Attempts = []notify.Attempt{{Channel: "ops", OK: true}}
	e.finishIntent(in, t0.Add(time.Second))
	var mine []Decision
	for _, d := range sink.ds {
		if d.IntentID == in.ID {
			mine = append(mine, d)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("ledger: %+v", sink.ds)
	}
	q, s := mine[0], mine[1]
	if q.Step != LedQueued || q.Class != ClassBusiness || q.Evidence["reason"] != probe.ReasonHTTPStatus || len(q.Recipients) != 1 || !q.DetectedAt.Equal(t0) {
		t.Errorf("queued: %+v", q)
	}
	// The chain was never tried, so this one failed: told as a channel
	// problem, with what was tried.
	if s.Step != LedFailed || s.Class != ClassChannelDown || len(s.Receipts) != 1 || s.Receipts[0].Channel != "ops" {
		t.Errorf("last: %+v", s)
	}
}

// fullSink cannot save incidents while full is set (a full disk).
type fullSink struct {
	*memSink
	full bool
}

func (s *fullSink) SaveIncident(inc *Incident) error {
	if s.full {
		return errors.New("database or disk is full")
	}
	return s.memSink.SaveIncident(inc)
}

// An incident opened while the disk is full keeps a temporary id: its
// delivered alert credits it, so its recovery is still owed, and it gets
// its real id, with its queued alerts, once the disk has room again.
func TestIncidentWhileDiskFull(t *testing.T) {
	sink := &fullSink{memSink: newMemSink(), full: true}
	e := New(Deps{Sink: sink})
	ts, _ := tunables.New(nil)
	e.cfg = Config{Tunables: ts, Location: time.UTC, Policies: map[string]PolicySpec{"default": {Name: "default", Chain: []string{"ops"}, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}},
		Channels: map[string]ChannelSpec{"ops": {Channel: notify.Channel{Name: "ops", Kind: "log"}}}}
	inc := &Incident{Monitor: "web", Policy: "default"}
	e.saveIncident(inc)
	if inc.ID >= 0 || e.incidents[inc.ID] != inc {
		t.Fatalf("temporary id: %d", inc.ID)
	}
	temp := inc.ID
	in := &Intent{IncidentID: temp, Kind: "PROBLEM", Policy: "default", CreatedAt: t0, DueAt: t0, Message: notify.Message{Kind: "PROBLEM", IncidentID: temp}}
	e.enqueue(in)
	e.credit(in)
	if inc.State.Told == 0 {
		t.Fatal("a delivered alert must credit the unsaved incident")
	}
	e.saveIncident(inc)
	if inc.ID != temp {
		t.Fatalf("still full: id %d", inc.ID)
	}
	sink.full = false
	e.saveIncident(inc)
	if inc.ID <= 0 || e.incidents[inc.ID] != inc || e.incidents[temp] != nil || in.IncidentID != inc.ID || in.Message.IncidentID != inc.ID {
		t.Fatalf("real id: %d, intent %d", inc.ID, in.IncidentID)
	}
}

// A service that failed on its own just before a local outage, and timed
// out once the cut began, did not fail with the outage: its incident is not
// taken back. One that failed at the network level is.
func TestBackdateKeepsServiceFailures(t *testing.T) {
	e, _ := ledgerEngine(t, nil)
	now := t0.Add(time.Hour)
	for _, n := range []string{"shop", "api"} {
		e.states[n] = &monRuntime{LastReason: probe.ReasonTimeout, Scope: netscope.WAN, Family: FamilyV4, LastChange: now.Add(-50 * time.Second),
			State: MonState{Avail: StatusCritical, Hard: true}}
		e.monitors[n] = &MonitorSpec{Name: n}
	}
	e.incidents[1] = &Incident{ID: 1, Monitor: "shop", Reason: probe.ReasonTimeout, openClass: reasonClass(probe.ReasonHTTPStatus), State: IncidentState{OpenedAt: now.Add(-50 * time.Second)}}
	e.incidents[2] = &Incident{ID: 2, Monitor: "api", Reason: probe.ReasonTimeout, openClass: reasonClass(probe.ReasonTimeout), State: IncidentState{OpenedAt: now.Add(-50 * time.Second)}}
	e.byMonitor["shop"], e.byMonitor["api"] = e.incidents[1], e.incidents[2]
	a := Anomaly{Kind: AnomalyUplink, Family: FamilyV4, Since: now}
	e.localnet.cfg.Enabled = true
	e.outageSeen = map[string]bool{}
	e.localnet.f(FamilyV4).uplink = &a
	e.backdate(a, now)
	if !e.byMonitor["shop"].State.Open() {
		t.Error("the service failure was taken back")
	}
	if inc := e.byMonitor["api"]; inc != nil && inc.State.Open() {
		t.Error("the network failure was not taken back")
	}
}

// A system notice that could not be sent during a local outage (this
// host crashed and restarted meanwhile) is told in the outage's summary,
// even when the outage was short enough to end quietly.
func TestOutageSummaryTellsWhatCouldNotBeSent(t *testing.T) {
	e, _ := ledgerEngine(t, nil)
	now := t0.Add(time.Hour)
	e.openSystem(SystemLocalNetwork, now, "the network is down")
	crash := &Intent{Kind: "SYSTEM", System: true, Policy: "default", CreatedAt: now.Add(10 * time.Second), DueAt: now,
		Message: notify.Message{Kind: "SYSTEM", Body: "Unagikuma exited abnormally around 10:00\nmore"}}
	e.enqueue(crash)
	crash.Delivery.Begin(0)
	crash.Delivery.Finish(0, false, "timeout", now.Add(20*time.Second), 0, 0)
	e.finishIntent(crash, now.Add(20*time.Second))
	e.outage = &outageEnd{since: now, end: now.Add(time.Minute), back: now.Add(time.Minute), deadline: now}
	e.finishOutage(now.Add(2 * time.Minute))
	var body string
	for _, in := range e.intents {
		if in.Kind == "SYSTEM" && in != crash {
			body = in.Message.Body
		}
	}
	if !strings.Contains(body, "Could not be sent meanwhile: Unagikuma exited abnormally around 10:00") || strings.Contains(body, "more") {
		t.Fatalf("summary %q", body)
	}
}

// When only the resolver failed, the summary says so rather than "this
// host lost its network".
func TestDNSOutageSummary(t *testing.T) {
	e, _ := ledgerEngine(t, nil)
	now := t0.Add(time.Hour)
	e.openSystem(SystemLocalNetwork, now, "dns")
	e.outage = &outageEnd{since: now, end: now.Add(10 * time.Minute), back: now.Add(10 * time.Minute), deadline: now, dnsOnly: true}
	e.finishOutage(now.Add(11 * time.Minute))
	var body string
	for _, in := range e.intents {
		if in.Kind == "SYSTEM" {
			body = in.Message.Body
		}
	}
	if !strings.HasPrefix(body, "This host's DNS resolver did not answer") {
		t.Fatalf("summary %q", body)
	}
}
