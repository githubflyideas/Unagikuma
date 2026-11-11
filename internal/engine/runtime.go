package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/ratelimit"
	"github.com/githubflyideas/unagikuma/internal/schedule"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// MonitorSpec is the engine's view of one monitor, built from the
// configuration by the wiring layer.
type MonitorSpec struct {
	Name  string
	Title string
	Group string
	Tags  []string
	Probe probe.Spec
	// Handler is an event handler script run on state changes.
	Handler string
	Depth   string // L0..L3 as advertised
	Policy  string // notification policy ("" = default)
	Team    string // owning team ("" = the built-in admin team)

	Interval        time.Duration
	Retry           time.Duration // interval while SOFT failing
	Attempts        int
	RecoverAttempts int
	// DegradedAttempts: consecutive slow rounds before DEGRADED (0 = Attempts).
	DegradedAttempts int
	// SlowAfter marks a round slow when its latency exceeds it (0 = off).
	SlowAfter time.Duration

	// Scope is lan or wan; "" or "auto" derives it from the probed address.
	Scope  netscope.Scope
	Quorum QuorumRule
	Paused bool

	// Push monitors are not probed: they expect a call to Push within
	// PushPeriod + PushGrace.
	Push       bool
	PushPeriod time.Duration
	PushGrace  time.Duration

	// Nodes are the participants of a cluster monitor (this node included
	// when it probes); empty means this node judges alone.
	Nodes []string
	// PartialEscalate: a partial failure lasting this long opens a
	// low-priority regional incident (0 = never).
	PartialEscalate time.Duration
	// TimeSensitive checks (time servers, certificate expiry) abstain on
	// nodes whose clock is off.
	TimeSensitive bool
	// Parents are the monitors this one depends on: while one of them is
	// down, this monitor's failure is not notified (UNREACHABLE).
	Parents []string

	// Host groups monitors checking one device (device= or the probed
	// host); a Ping monitor decides the host state. Area is the
	// configuration section. They only shape reports.
	Host    string
	Address string
	Area    string
	Ping    bool

	// DefHash changes when the check itself changes: the monitor starts
	// over as PENDING and its incident is closed.
	DefHash string
	// SecretError is set when a referenced secret cannot be resolved: the
	// monitor abstains (secret_unavailable) instead of probing.
	SecretError string
}

// ChannelSpec is a notification channel with secrets resolved.
type ChannelSpec struct {
	notify.Channel
	Retries int
	// Period limits the channel to a weekly period (nil = always);
	// Outside inverts it.
	Period  *schedule.Period
	Outside bool
	// Members makes a contact group: every member is notified at once.
	Members []string
	// Reachability of the channel's endpoint, for pausing it during a local
	// network outage instead of failing it.
	Scope  netscope.Scope
	Family Family
	OnLink bool
	ByName bool
	// NoRecipients explains why nobody is left to send to (all of them
	// left or keep bouncing): each attempt fails at once with it, so the
	// policy moves on to its next channel.
	NoRecipients string
	// NoReport: the channel does not take the daily status report
	// (report=no). ReportOpts, when set, are its options for the report:
	// the recipients who switched it off are left out.
	NoReport   bool
	ReportOpts map[string]string
}

// PolicySpec is a resolved POLICY record.
type PolicySpec struct {
	Name   string
	Chain  []string
	Mode   string // fallback | all
	Notify NotifyPolicy
	// Network receives the summaries of network-class failures (all at
	// once); empty: they go to Chain with the rest.
	Network []string
	// Escalate notifies more channels while an incident stays open and
	// unacknowledged.
	Escalate []EscalationStep
	// Mute: nothing is sent (mute=yes on a monitor).
	Mute bool
	// OnFail are chains each sent the notice on their own when it could not
	// be delivered through the policy's chain: a team's are the system
	// channels and the team's administrators.
	OnFail [][]string
}

// EscalationStep sends to Channels once an incident is After old.
type EscalationStep struct {
	After    time.Duration
	Channels []string
}

// Config is everything the engine runs with; Apply swaps it atomically.
type Config struct {
	SourceNode       string
	ObservationSites map[string][]string
	Serial           uint32
	Instance         string
	Lang             string // message language: zh | en
	BaseURL          string // for links in messages ("" = none)
	Location         *time.Location
	Monitors         []MonitorSpec
	Policies         map[string]PolicySpec
	Channels         map[string]ChannelSpec
	Tunables         tunables.Set
	// LocalDetect enables local network anomaly detection on this node.
	LocalDetect bool
	// Anchors are explicit lan/wan anchors of this node.
	Anchors []AnchorSpec
	// Self is this node's name in a cluster ("" = single node).
	Self string
	// Nodes are the agents of the cluster.
	Nodes []NodeSpec
	// Maints are the maintenance windows.
	Maints []MaintSpec
	// ReportTeams receive the daily status report, each on the monitors
	// it owns; empty: one report on everything, through ReportPolicy.
	ReportTeams []ReportTeam
}

// ReportTeam is a team receiving the daily status report and the policy
// it is sent through.
type ReportTeam struct {
	Name   string
	Policy string
}

// AnchorSpec is an explicit anchor address.
type AnchorSpec struct {
	Kind AnchorKind // lan | wan
	Addr string     // IP literal
}

// Deps are the engine's collaborators. Probe and Send perform network I/O
// and are called from worker goroutines; Sink is called from the engine
// loop only.
type Deps struct {
	Clock     func() time.Time
	Probe     func(ctx context.Context, s probe.Spec) probe.Result
	Send      func(ctx context.Context, ch notify.Channel, m *notify.Message) notify.Attempt
	Sink      Sink
	HostClock func() (hostclock.Reading, bool)
	Gateways  func() ([]netscope.Gateway, error)
	// InContainer reports whether the default gateway is a container bridge.
	InContainer bool
	// Remote sends probe requests to agents (nil without a cluster).
	Remote Remote
	Log    *slog.Logger
}

// Engine runs rounds, judges them and drives incidents and notifications.
// All state is owned by the loop goroutine; other goroutines interact
// through do().
type Engine struct {
	deps Deps
	log  *slog.Logger
	self string // this node's name

	cfg      Config
	monitors map[string]*MonitorSpec
	states   map[string]*monRuntime

	sched    *Scheduler
	localnet *LocalNet
	watchdog *Watchdog

	incidents map[int64]*Incident  // open or reopenable incidents
	byMonitor map[string]*Incident // current real incident per monitor (normal kind)
	lowByMon  map[string]*Incident // DEGRADED low-priority incident per monitor
	system    map[string]*Incident // system incidents by key
	regional  map[string]*Incident // regional (lasting partial) incident per monitor
	children  map[string][]string  // dependents of each monitor
	maintOf   map[string]string    // monitor → open maintenance window
	maintOpen map[string]time.Time // open windows → their end
	intents   map[string]*Intent   // undelivered intents
	// tempIncident numbers incidents the store could not save yet (< 0).
	tempIncident int64
	chanRate     map[string]*ratelimit.Bucket
	sentAt       map[string][]time.Time  // per channel: sends in the backstop window
	unsent       []unsentNote            // system notices that could not be sent (told after a local outage)
	folded       map[string]*backstopBuf // per policy: alerts the backstop held back

	sem   chan struct{}
	stats selfStats
	pps   *ratelimit.Bucket
	// nextStart is when the next ping-like round to an address may start
	// (probe.same-target-gap); loop goroutine only.
	nextStart map[string]time.Time
	// share holds recent probe executions for share=yes monitors;
	// shareWindow is probe.share-window, readable from any goroutine.
	share       sharing
	shareWindow atomic.Int64
	results     chan roundResult

	nodes    map[string]*nodeRuntime // agents by name
	agentRT  map[string]*monRuntime  // agent mode: per-monitor network caches
	attempts chan attemptResult
	// runCtx ends with Run; background work (event handlers) uses it.
	runCtx context.Context
	// handling marks monitors whose event handler is running.
	handling map[string]bool
	cmds     chan func()

	gate gateCache
	// outage is the local outage that just ended, until the monitors it
	// hid have been re-checked and its summary is sent.
	outage *outageEnd
	// outageDNS: the running local outage is only the resolver's.
	outageDNS bool
	// outageSeen are the monitors a running local outage has held.
	outageSeen map[string]bool

	nextReminder     time.Time
	nextReport       time.Time
	booted           time.Time
	started          atomic.Bool
	applied          bool
	anchors          map[string]AnchorSpec
	exit             exitState // last ping of localnet.exit-target
	ifCache          []netscope.Iface
	ifCacheAt        time.Time
	lastInterruption Interruption
	stopped          chan struct{}
}

// monRuntime is the live state of one monitor.
type monRuntime struct {
	State       MonState
	DefHash     string
	LastReason  string
	LastDetail  string
	Running     bool // a round is in flight
	Due         time.Time
	LastCheck   time.Time
	LastChange  time.Time
	LastResult  probe.Result
	LastVerdict Verdict
	LastVotes   []VoteView // per-node votes of the last cluster round
	// PartialKey/PartialSince: the failing nodes of a running partial
	// failure and since when (for the regional escalation).
	PartialKey   string
	PartialSince time.Time
	Scope        netscope.Scope // effective (auto resolved)
	Family       Family
	Domain       string
	OnLink       bool
	ByName       bool
	LastPush     time.Time
	// Since is when this definition started being watched (created,
	// redefined or resumed): a PUSH monitor gets a full period from then.
	Since      time.Time
	PushScope  netscope.Scope
	CheckNowAt time.Time
	// Dependencies: a held HARD failure and whether a parent explains it.
	Dep         *depHold
	Unreachable bool
}

// NodeName is the name this node votes under in single-node mode.
const NodeName = "local"

// New creates an engine. Call Restore (optional) and Apply before Run.
func New(d Deps) *Engine {
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.HostClock == nil {
		d.HostClock = hostclock.Read
	}
	if d.Gateways == nil {
		d.Gateways = netscope.DefaultGateways
	}
	now := d.Clock()
	ts := tunables.Set{}
	e := &Engine{
		deps:      d,
		log:       d.Log,
		self:      NodeName,
		monitors:  map[string]*MonitorSpec{},
		states:    map[string]*monRuntime{},
		sched:     NewScheduler(),
		localnet:  NewLocalNet(LocalNetConfigFrom(ts), now),
		watchdog:  NewWatchdog(SuspendConfigFrom(ts)),
		incidents: map[int64]*Incident{},
		byMonitor: map[string]*Incident{},
		lowByMon:  map[string]*Incident{},
		system:    map[string]*Incident{},
		regional:  map[string]*Incident{},
		nodes:     map[string]*nodeRuntime{},
		agentRT:   map[string]*monRuntime{},
		intents:   map[string]*Intent{},
		chanRate:  map[string]*ratelimit.Bucket{},
		sentAt:    map[string][]time.Time{},
		folded:    map[string]*backstopBuf{},
		results:   make(chan roundResult, 256),
		attempts:  make(chan attemptResult, 64),
		handling:  map[string]bool{},
		cmds:      make(chan func(), 64),
		booted:    now,
		stopped:   make(chan struct{}),
	}
	e.applyTunables(ts)
	return e
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (e *Engine) now() time.Time { return e.deps.Clock() }

func (e *Engine) applyTunables(ts tunables.Set) {
	lc := LocalNetConfigFrom(ts)
	lc.Enabled = lc.Enabled && e.cfg.LocalDetect
	e.localnet.SetConfig(lc)
	e.watchdog.SetConfig(SuspendConfigFrom(ts))
	probe.SetExecLimit(ts.Int("probe.exec-concurrency"))
	n := ts.Int("probe.max-concurrency")
	if e.sem == nil || cap(e.sem) != n {
		e.sem = make(chan struct{}, max(1, n))
	}
	e.shareWindow.Store(int64(ts.Duration("probe.share-window")))
	pps := float64(ts.Int("probe.max-pps"))
	if e.pps == nil {
		e.pps = ratelimit.New(pps, int(pps))
	} else {
		e.pps.SetRate(pps, int(pps))
	}
	r := ts.Rate("notify.channel-rate")
	for _, b := range e.chanRate {
		b.SetRate(r, max(1, int(r*60)))
	}
}

// do runs f on the loop goroutine and waits for it.
func (e *Engine) do(f func()) bool {
	done := make(chan struct{})
	select {
	case e.cmds <- func() { f(); close(done) }:
	case <-e.stopped:
		return false
	}
	select {
	case <-done:
		return true
	case <-e.stopped:
		return false
	}
}

// Run is the engine loop; it returns when ctx is done.
func (e *Engine) Run(ctx context.Context) {
	e.started.Store(true)
	ctx, cancel := context.WithCancel(ctx)
	e.runCtx = ctx
	var wg sync.WaitGroup
	defer func() {
		cancel()
		// Release goroutines blocked in do() before waiting for them: a
		// worker that reports through do() during shutdown must not keep
		// Run from returning.
		close(e.stopped)
		wg.Wait()
	}()

	tick := e.cfg.Tunables.Duration("suspend.tick")
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	anchorEvery := e.cfg.Tunables.Duration("localnet.anchor-interval")
	anchors := time.NewTicker(anchorEvery)
	defer anchors.Stop()
	wg.Add(1)
	go func() { defer wg.Done(); e.checkAnchors(ctx) }()

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		e.dispatchDue(ctx, &wg)
		e.dispatchIntents(ctx, &wg)
		wait := time.Hour
		if next, ok := e.sched.Next(); ok {
			wait = max(0, next.Sub(e.now()))
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-ticker.C:
			e.onTick()
			if d := e.cfg.Tunables.Duration("suspend.tick"); d != tick {
				tick = d
				ticker.Reset(d)
			}
		case <-anchors.C:
			wg.Add(1)
			go func() { defer wg.Done(); e.checkAnchors(ctx) }()
			if d := e.cfg.Tunables.Duration("localnet.anchor-interval"); d != anchorEvery {
				anchorEvery = d
				anchors.Reset(d)
			}
		case r := <-e.results:
			e.onResult(r)
		case a := <-e.attempts:
			e.onAttempt(a)
		case f := <-e.cmds:
			f()
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// onTick runs the periodic work: clock watchdog, local network evaluation,
// incident timers, overload, push deadlines, daily reminder.
func (e *Engine) onTick() {
	e.finishOutage(e.now())
	now := e.now()
	hc, ok := e.deps.HostClock()
	if in, jump := e.watchdog.Tick(ClockSample{At: now, Host: hc, HostOK: ok}); in != nil {
		e.onInterruption(*in, now)
	} else if jump != nil {
		e.event(Event{Type: EventSystem, Severity: "info", Message: "wall clock changed", Data: map[string]any{"msg": "clock_changed", "delta_s": int(jump.Delta.Seconds())}})
	}
	for _, ev := range e.localnet.Evaluate(now) {
		e.onLocalNet(ev, now)
	}
	if since, ok := e.localnet.Release(now); ok {
		e.releaseHold(since, now)
	}
	if ev := e.watchdog.EvaluateOverload(now); ev != nil {
		e.onOverload(*ev, now)
	}
	e.checkPushDeadlines(now)
	e.expireDeps(now)
	e.checkMaint(now)
	e.checkNodes(now)
	e.advanceIncidents(now)
	e.escalate(now)
	e.dailyReminder(now)
	e.dailyReport(now)
}

// sortedMonitorNames returns monitor names in a stable order.
func (e *Engine) sortedMonitorNames() []string {
	names := make([]string, 0, len(e.monitors))
	for n := range e.monitors {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
