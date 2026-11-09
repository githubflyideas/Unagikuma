package engine

import (
	"context"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// NodeSpec is a remote probing node (an agent) of the cluster.
type NodeSpec struct {
	Name    string
	Title   string
	Region  string
	Probe   bool          // takes part in rounds
	Policy  string        // policy of its system incidents ("" = system)
	Timeout time.Duration // disconnected this long opens an offline incident
}

// Remote sends probe requests to agents. Probe must not block; it reports
// false when the node is not connected.
type Remote interface {
	Probe(node string, req ProbeRequest) bool
}

// ProbeRequest asks an agent to run one round of a monitor. The agent
// already holds the monitor's definition; DefHash proves it is the same.
type ProbeRequest struct {
	Round   string `json:"round"`
	Monitor string `json:"monitor"`
	DefHash string `json:"def_hash"`
}

// RemoteVote is an agent's judged answer to a ProbeRequest.
type RemoteVote struct {
	Node     string       `json:"node"`
	Round    string       `json:"round"`
	Monitor  string       `json:"monitor"`
	DefHash  string       `json:"def_hash"`
	Result   probe.Result `json:"result"`
	Vote     probe.Vote   `json:"vote"`
	Reason   string       `json:"reason"`
	Excluded bool         `json:"excluded,omitempty"`
}

// NodeStatus is what the transport knows about an agent.
type NodeStatus struct {
	Node     string        `json:"node"`
	Online   bool          `json:"online"`
	Since    time.Time     `json:"since"`     // when Online last changed
	LastSeen time.Time     `json:"last_seen"` // last message received
	Skew     time.Duration `json:"skew"`      // agent clock minus ours
	RTT      time.Duration `json:"rtt"`
	Version  string        `json:"version,omitempty"`
	Platform string        `json:"platform,omitempty"`
	Serial   uint32        `json:"serial"` // configuration serial the agent runs
	Addr     string        `json:"addr,omitempty"`
	Health   *Health       `json:"health,omitempty"` // the agent's view of its own network
	// Scripts are the node's check scripts, for EXEC monitors.
	Scripts []probe.Script `json:"scripts,omitempty"`
	// Unsupported are monitors this node is meant to check but cannot:
	// it runs an older release without their check type.
	Unsupported []string `json:"unsupported,omitempty"`
	// Listen is where other nodes can reach this one ("" for probe-only
	// agents, which cannot be checked by their peers).
	Listen          string `json:"listen,omitempty"`
	PeerFingerprint string `json:"peer_fingerprint,omitempty"`
	// Reach is this node's own view: which other nodes it can reach.
	Reach map[string]bool `json:"reach,omitempty"`
	// Gateway is how this node's last notifications went out, per channel.
	Gateway []GatewayState `json:"gateway,omitempty"`
	// Verdict is this site's local peer-path status, never a global verdict.
	Verdict   string `json:"verdict,omitempty"`
	Witnesses int    `json:"witnesses,omitempty"` // nodes that cannot reach it
}

// GatewayState is the outcome of a node's last notification on a channel:
// whether its uplink to the notification gateway (SMS, webhook, mail) works.
type GatewayState struct {
	Channel string    `json:"channel"`
	Kind    string    `json:"kind"`
	OK      bool      `json:"ok"`
	At      time.Time `json:"at"`
	Error   string    `json:"error,omitempty"`
}

// Node verdicts.
const (
	NodeOK          = "ok"
	NodeUnreachable = "unreachable"
	NodeDown        = "down"
	NodeUnknown     = "unknown"
)

// VoteView is one node's latest vote on a monitor, for the UI.
type VoteView struct {
	Node      string  `json:"node"`
	Vote      string  `json:"vote"` // ok | fail | abstain | absent | offline
	Reason    string  `json:"reason,omitempty"`
	Detail    string  `json:"detail,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Excluded  bool    `json:"excluded,omitempty"`
}

// nodeRuntime is the engine's view of one agent.
type nodeRuntime struct {
	status       NodeStatus
	offlineSince time.Time // zero while online
}

// IncidentRegional is the low-priority incident of a monitor that keeps
// failing from some nodes while the others reach it.
const IncidentRegional = "regional"

// clustered reports whether a monitor is judged by more than this node.
func clustered(m *MonitorSpec) bool { return len(m.Nodes) > 0 }

func (e *Engine) participates(m *MonitorSpec) bool {
	if !clustered(m) {
		return true
	}
	for _, n := range m.Nodes {
		if n == e.self {
			return true
		}
	}
	return false
}

// RemoteVote discards legacy round replies. Observations use a separate path.
func (e *Engine) RemoteVote(_ RemoteVote) {}

// NodeState records an agent's connection state (any goroutine).
func (e *Engine) NodeState(s NodeStatus) {
	e.do(func() { e.onNodeState(s) })
}

// nodeLabel names a node in messages: "title (name)", or the name.
func (e *Engine) nodeLabel(name string) string {
	for _, n := range e.cfg.Nodes {
		if n.Name == name && n.Title != "" {
			return n.Title + " (" + n.Name + ")"
		}
	}
	return name
}

func (e *Engine) onNodeState(s NodeStatus) {
	nr := e.nodes[s.Node]
	if nr == nil {
		return // not a configured node
	}
	now := e.now()
	was := nr.status.Online
	// The verdict is the engine's judgement, not the report's.
	s.Verdict, s.Witnesses = nr.status.Verdict, nr.status.Witnesses
	nr.status = s
	switch {
	case s.Online && !was:
		nr.offlineSince = time.Time{}
		e.event(Event{Type: EventSystem, Severity: "info", Message: "node " + s.Node + " connected", Data: map[string]any{"msg": "node_connected", "node": s.Node, "version": s.Version}})
		e.resolveSystem(nodeOfflineKey(s.Node), now, e.text("node_back", e.nodeLabel(s.Node)))
	case !s.Online && was:
		nr.offlineSince = now
		e.event(Event{Type: EventSystem, Severity: "warning", Message: "node " + s.Node + " disconnected", Data: map[string]any{"msg": "node_disconnected", "node": s.Node}})
	}
}

func nodeOfflineKey(node string) string { return SystemNodeOffline + ":" + node }

// SystemNodeOffline is the system incident of an agent that stays away.
const SystemNodeOffline = "node_offline"

// applyNodes keeps the node table in line with the configuration. A node
// counts as disconnected from the moment it is configured, so one that never
// connects is reported after its timeout too.
func (e *Engine) applyNodes(specs []NodeSpec, now time.Time) {
	keep := map[string]bool{}
	for _, n := range specs {
		if n.Name == e.self {
			continue // this server is never a remote agent of itself
		}
		keep[n.Name] = true
		if e.nodes[n.Name] == nil {
			e.nodes[n.Name] = &nodeRuntime{status: NodeStatus{Node: n.Name, Since: now}, offlineSince: now}
		}
	}
	for name := range e.nodes {
		if !keep[name] {
			delete(e.nodes, name)
			e.resolveSystem(nodeOfflineKey(name), now, "")
		}
	}
}

// SystemMinority is the system incident of a node that cannot reach more
// than half of the cluster: it cannot tell who is down, so it says so
// instead of reporting the others.
const SystemMinority = "minority"

// checkNodes reports this site's own path, never a majority's node verdict.
func (e *Engine) checkNodes(now time.Time) {
	e.resolveSystem(SystemMinority, now, "") // retire pre-upgrade minority state
	grace := e.cfg.Tunables.Duration("cluster.startup-grace")
	for _, n := range e.cfg.Nodes {
		if n.Name == e.self {
			continue
		}
		nr := e.nodes[n.Name]
		if nr == nil {
			continue
		}
		nr.status.Witnesses = 0
		if nr.status.Online || nr.offlineSince.IsZero() {
			nr.status.Verdict = NodeOK
			continue
		}
		nr.status.Verdict = NodeUnreachable
		timeout := n.Timeout
		if timeout <= 0 {
			timeout = e.cfg.Tunables.Duration("cluster.node-timeout")
		}
		if now.Sub(nr.offlineSince) < timeout {
			continue
		}
		if grace > 0 && !e.booted.IsZero() && now.Sub(e.booted) < grace && !nr.offlineSince.After(e.booted) {
			continue
		}
		key := nodeOfflineKey(n.Name)
		if inc := e.system[key]; inc != nil && inc.State.Open() {
			continue
		}
		pol := n.Policy
		if pol == "" {
			pol = "system"
		}
		e.openSystemAs(key, pol, e.text("peer_unreachable_title", e.nodeLabel(n.Name)), e.text("peer_unreachable", e.nodeLabel(n.Name)), now)
	}
}

// ClearClusterRoleIncidents resolves master-era system incidents after this
// server steps down and rejoins as a member: minority and node-offline of
// the old role must not stay open under the new one.
func (e *Engine) ClearClusterRoleIncidents() {
	f := func() {
		now := e.now()
		e.resolveSystem(SystemMinority, now, "")
		for key := range e.system {
			if key == SystemMinority || strings.HasPrefix(key, SystemNodeOffline+":") {
				e.resolveSystem(key, now, "")
			}
		}
	}
	if !e.started.Load() {
		f()
		return
	}
	e.do(f)
}

// Nodes returns the configured agents with their latest state.
func (e *Engine) Nodes() []NodeStatus {
	var out []NodeStatus
	e.do(func() {
		for _, n := range e.cfg.Nodes {
			if nr := e.nodes[n.Name]; nr != nil {
				out = append(out, nr.status)
			}
		}
	})
	return out
}

// Self is this site's configured node name.
func (e *Engine) Self() string {
	var s string
	e.do(func() { s = e.self })
	return s
}

// AgentVote is an agent's judged answer to one probe request.
type AgentVote struct {
	Result   probe.Result
	Vote     probe.Vote
	Reason   string
	Excluded bool
}

// ProbeFor runs one probe the master asked for and judges it with this
// node's own health: an agent runs an engine without monitors only for its
// local network detector and suspend watchdog. Safe for any goroutine.
func (e *Engine) ProbeFor(ctx context.Context, name string, spec probe.Spec, scope netscope.Scope) AgentVote {
	start := e.now()
	res := e.runProbe(ctx, spec)
	end := e.now()
	out := AgentVote{Result: res, Vote: res.Vote, Reason: res.Reason}
	e.do(func() {
		rt := e.agentRT[name]
		if rt == nil {
			rt = &monRuntime{}
			e.agentRT[name] = rt
		}
		nv := e.judgeLocal(rt, scope, spec, roundResult{name: name, start: start, end: end, res: res})
		rt.LastResult = res // the agent's own internet-exit check reads it
		out.Vote, out.Reason, out.Excluded = nv.Vote, nv.Reason, nv.Excluded
	})
	return out
}

// ForgetAgentMonitors drops the per-monitor caches of an agent for monitors
// it no longer runs.
func (e *Engine) ForgetAgentMonitors(keep map[string]bool) {
	e.do(func() {
		for n := range e.agentRT {
			if !keep[n] {
				delete(e.agentRT, n)
			}
		}
	})
}
