package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// memSink keeps what the engine persists, for assertions.
type memSink struct {
	mu        sync.Mutex
	nextID    int64
	incidents map[int64]Incident
	events    []Event
	evidence  map[string]int // "monitor/node" → count
}

func newMemSink() *memSink {
	return &memSink{incidents: map[int64]Incident{}, evidence: map[string]int{}}
}

func (s *memSink) SaveState(StateRecord)   {}
func (s *memSink) DeleteState(string)      {}
func (s *memSink) Verdict(VerdictRecord)   {}
func (s *memSink) Delivery(DeliveryRecord) {}
func (s *memSink) Counter(string, int)     {}
func (s *memSink) SaveIntent(*Intent) error {
	return nil
}
func (s *memSink) Evidence(name, node string, _ time.Time, _ probe.Result) {
	s.mu.Lock()
	s.evidence[name+"/"+node]++
	s.mu.Unlock()
}
func (s *memSink) SaveIncident(inc *Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inc.ID == 0 {
		s.nextID++
		inc.ID = s.nextID
	}
	s.incidents[inc.ID] = *inc
	return nil
}
func (s *memSink) Event(ev Event) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *memSink) open(kind, key string) (Incident, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inc := range s.incidents {
		if inc.Kind == kind && (inc.Monitor == key || inc.System == key) && inc.State.Open() {
			return inc, true
		}
	}
	return Incident{}, false
}

// fakeCluster answers probe requests for agents and the local probe from
// one table of per-node votes.
type fakeCluster struct {
	mu     sync.Mutex
	e      *Engine
	votes  map[string]probe.Vote // node → vote for every monitor
	silent map[string]bool       // node accepts requests but never answers
	asked  map[string]int
}

func (f *fakeCluster) set(node string, v probe.Vote) {
	f.mu.Lock()
	f.votes[node] = v
	f.mu.Unlock()
}

func (f *fakeCluster) result(node string) probe.Result {
	f.mu.Lock()
	v, ok := f.votes[node]
	f.mu.Unlock()
	ev := &probe.Evidence{Peer: "10.0.0.1:80"}
	if !ok || v == probe.VoteOK {
		return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Latency: 2 * time.Millisecond, Evidence: ev}
	}
	return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonTimeout, Detail: "timeout", Evidence: ev}
}

func (f *fakeCluster) Probe(node string, req ProbeRequest) bool {
	f.mu.Lock()
	f.asked[node]++
	silent := f.silent[node]
	f.mu.Unlock()
	if silent {
		return true
	}
	go func() {
		r := f.result(node)
		f.e.RemoteVote(RemoteVote{Node: node, Round: req.Round, Monitor: req.Monitor, DefHash: req.DefHash, Result: r, Vote: r.Vote, Reason: r.Reason})
	}()
	return true
}

type clusterEnv struct {
	t    *testing.T
	e    *Engine
	sink *memSink
	fc   *fakeCluster
}

func newClusterEnv(t *testing.T, cfg Config) *clusterEnv {
	t.Helper()
	sink := newMemSink()
	fc := &fakeCluster{votes: map[string]probe.Vote{}, silent: map[string]bool{}, asked: map[string]int{}}
	e := New(Deps{
		Probe: func(ctx context.Context, s probe.Spec) probe.Result { return fc.result(cfg.Self) },
		Send: func(context.Context, notify.Channel, *notify.Message) notify.Attempt {
			return notify.Attempt{OK: true}
		},
		Sink:      sink,
		HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways:  func() ([]netscope.Gateway, error) { return nil, nil },
		Remote:    fc,
	})
	fc.e = e
	ts, err := tunables.New(map[string]string{
		"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "cluster.vote-grace": "300ms", "cluster.startup-grace": "0s", "localnet.enabled": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tunables = ts
	e.Apply(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &clusterEnv{t: t, e: e, sink: sink, fc: fc}
}

func (c *clusterEnv) online(nodes ...string) {
	for _, n := range nodes {
		c.e.NodeState(NodeStatus{Node: n, Online: true, Since: time.Now()})
	}
}

func (c *clusterEnv) waitRow(name string, what string, cond func(MonitorRow) bool) MonitorRow {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, ok := c.e.Row(name)
		if ok && cond(r) {
			return r
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("%s: last row %+v votes %+v", what, r, r.Votes)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func clusterMonitor(nodes ...string) MonitorSpec {
	return MonitorSpec{
		Name: "web", Title: "web", Probe: probe.Spec{Name: "web", Type: "TCP", Target: "10.0.0.1:80", Timeout: 100 * time.Millisecond},
		Interval: 40 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DefHash: "d1", Nodes: nodes,
		Quorum: QuorumRule{Spec: "majority"},
	}
}

func clusterCfg(m MonitorSpec, agents ...string) Config {
	cfg := Config{Self: "m", Monitors: []MonitorSpec{m}}
	for _, a := range agents {
		cfg.Nodes = append(cfg.Nodes, NodeSpec{Name: a, Probe: true, Timeout: time.Hour})
	}
	return cfg
}

func TestSitesDoNotVote(t *testing.T) {
	c := newClusterEnv(t, clusterCfg(clusterMonitor("m", "a", "b"), "a", "b"))
	c.online("a", "b")
	c.fc.set("m", probe.VoteFail)
	r := c.waitRow("web", "local failure despite healthy peers", func(r MonitorRow) bool { return r.Status == StatusCritical })
	if r.Partial || r.RegionalID != 0 || r.Quorum != nil {
		t.Fatalf("legacy judgement: %+v", r)
	}
	if _, ok := c.sink.open(IncidentNormal, "web"); !ok {
		t.Fatal("no local incident")
	}
	c.fc.set("a", probe.VoteFail)
	c.fc.set("b", probe.VoteFail)
	c.fc.set("m", probe.VoteOK)
	c.waitRow("web", "local recovery despite remote failures", func(r MonitorRow) bool { return r.Status == StatusOK })
	c.fc.mu.Lock()
	asked := len(c.fc.asked)
	c.fc.mu.Unlock()
	if asked != 0 {
		t.Fatal("remote scheduling remains")
	}
}
func TestSiteContinuesWithoutPeers(t *testing.T) {
	c := newClusterEnv(t, clusterCfg(clusterMonitor("m", "a", "b"), "a", "b"))
	c.fc.set("m", probe.VoteFail)
	c.waitRow("web", "isolated failure", func(r MonitorRow) bool { return r.Status == StatusCritical })
	c.fc.set("m", probe.VoteOK)
	c.waitRow("web", "isolated recovery", func(r MonitorRow) bool { return r.Status == StatusOK })
}
func TestMonitorAssignedElsewhereNotScheduled(t *testing.T) {
	c := newClusterEnv(t, clusterCfg(clusterMonitor("a", "b"), "a", "b"))
	time.Sleep(150 * time.Millisecond)
	r, _ := c.e.Row("web")
	if !r.LastCheck.IsZero() {
		t.Fatalf("non-local monitor checked: %+v", r)
	}
}
func TestRemoteClockCannotChangeLocalVerdict(t *testing.T) {
	m := clusterMonitor("m", "a", "b")
	m.TimeSensitive = true
	c := newClusterEnv(t, clusterCfg(m, "a", "b"))
	c.e.NodeState(NodeStatus{Node: "a", Online: true, Skew: 5 * time.Second})
	c.fc.set("m", probe.VoteFail)
	c.waitRow("web", "own time-sensitive failure", func(r MonitorRow) bool { return r.Status == StatusCritical })
}
func TestClusterNodeOffline(t *testing.T) {
	cfg := clusterCfg(clusterMonitor("m", "a"), "a")
	cfg.Nodes[0].Timeout = 100 * time.Millisecond
	c := newClusterEnv(t, cfg)
	c.waitOpen(nodeOfflineKey("a"), true)
	if nodeVerdict(c.e, "a") != NodeUnreachable {
		t.Fatal("path must not claim node death")
	}
	c.online("a")
	c.waitOpen(nodeOfflineKey("a"), false)
}

// waitOpen waits until a system incident is open (or not).
func (c *clusterEnv) waitOpen(key string, want bool) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := c.sink.open(IncidentSystem, key); ok == want {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("incident %s open=%v never happened; nodes %+v", key, want, c.e.Nodes())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func nodeVerdict(e *Engine, n string) string {
	for _, s := range e.Nodes() {
		if s.Node == n {
			return s.Verdict
		}
	}
	return ""
}

func TestMinorityDoesNotHideLocalPeerFailure(t *testing.T) {
	cfg := clusterCfg(clusterMonitor("m", "a", "b"), "a", "b")
	for i := range cfg.Nodes {
		cfg.Nodes[i].Timeout = 100 * time.Millisecond
	}
	c := newClusterEnv(t, cfg)
	c.waitOpen(nodeOfflineKey("a"), true)
	c.waitOpen(nodeOfflineKey("b"), true)
	if _, ok := c.sink.open(IncidentSystem, SystemMinority); ok {
		t.Fatal("obsolete minority incident")
	}
	for _, n := range []string{"a", "b"} {
		if nodeVerdict(c.e, n) != NodeUnreachable {
			t.Fatal(n, nodeVerdict(c.e, n))
		}
	}
}

func TestSingleNodeUnchanged(t *testing.T) {
	m := clusterMonitor()
	c := newClusterEnv(t, Config{Monitors: []MonitorSpec{m}})
	c.fc.set("", probe.VoteFail)
	r := c.waitRow("web", "single-node failure", func(r MonitorRow) bool { return r.Status == StatusCritical })
	if len(r.Votes) != 0 || r.Quorum != nil {
		t.Fatalf("single node rows carry no votes: %+v", r)
	}
	c.fc.mu.Lock()
	asked := len(c.fc.asked)
	c.fc.mu.Unlock()
	if asked != 0 {
		t.Fatal("single node must not ask agents")
	}
}

func TestProbeForAgentMode(t *testing.T) {
	c := newClusterEnv(t, Config{})
	c.fc.set("", probe.VoteFail)
	v := c.e.ProbeFor(context.Background(), "x", probe.Spec{Name: "x", Type: "TCP", Target: "10.0.0.9:22", Timeout: time.Second}, "")
	if v.Vote != probe.VoteFail || v.Reason != probe.ReasonTimeout || v.Excluded {
		t.Fatalf("%+v", v)
	}
}

func TestCheckNodesSkipsSelf(t *testing.T) {
	// After a promote the restored config may still list this server as a
	// NODE; it must not be judged offline forever.
	cfg := clusterCfg(clusterMonitor("m", "a"), "a", "self")
	cfg.Self = "self"
	c := newClusterEnv(t, cfg)
	c.online("a")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range c.e.Nodes() {
			if n.Node == "self" {
				t.Fatalf("self listed as a remote node: %+v", n)
			}
		}
		if _, ok := c.sink.open(IncidentSystem, nodeOfflineKey("self")); ok {
			t.Fatal("opened offline incident for self")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestForeignParentCannotSuppressLocalIncident(t *testing.T) {
	m := clusterMonitor("m")
	m.Parents = []string{"foreign-parent"}
	c := newClusterEnv(t, clusterCfg(m, "a"))
	c.fc.set("m", probe.VoteFail)
	c.waitRow("web", "local failure with foreign parent", func(r MonitorRow) bool { return r.IncidentID != 0 && r.Status == StatusCritical })
	if _, ok := c.sink.open(IncidentNormal, "web"); !ok {
		t.Fatal("foreign parent suppressed local incident")
	}
}
