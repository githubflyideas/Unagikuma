package cluster

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// Protocol version. Peers whose major versions match and whose minor
// versions differ by at most one can talk to each other, so the master and
// its agents can be upgraded one at a time.
const (
	ProtoMajor = 1
	ProtoMinor = 1
)

// ProtoVersion is this build's protocol version.
var ProtoVersion = fmt.Sprintf("%d.%d", ProtoMajor, ProtoMinor)

// Upgrade names the protocol in the HTTP upgrade handshake.
const Upgrade = "unagikuma-agent"

// ConnectPath is the master endpoint agents upgrade on.
const ConnectPath = "/api/v1/agent/connect"

// StatePath is where a standby fetches the master's state.
const StatePath = "/api/v1/agent/state"

// StateKeyHeader carries the passphrase of the state archive, which is
// generated per download and only ever travels over the pinned TLS.
const StateKeyHeader = "X-Unagikuma-State-Key"

// StateTermHeader carries the master's term with the state.
const StateTermHeader = "X-Unagikuma-Term"

// NodeHeader carries the agent's node name in the upgrade request.
const NodeHeader = "X-Unagikuma-Node"

// compatible reports whether a peer speaking version v can be served.
func compatible(v string) bool {
	maj, min, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	a, err1 := strconv.Atoi(maj)
	b, err2 := strconv.Atoi(min)
	if err1 != nil || err2 != nil || a != ProtoMajor {
		return false
	}
	return b >= ProtoMinor-1 && b <= ProtoMinor+1
}

// Message types.
const (
	msgHello   = "hello"   // agent → master, first message
	msgWelcome = "welcome" // master → agent, accepts the hello
	msgError   = "error"   // either way, then the connection closes
	msgConfig  = "config"  // master → agent
	msgProbe   = "probe"   // master → agent
	msgVote    = "vote"    // agent → master
	msgStatus  = "status"  // agent → master
	msgPing    = "ping"    // master → agent
	msgPong    = "pong"    // agent → master
	msgState   = "state"   // master → standby: fetch the state now (1.1)
	msgPromote = "promote" // master → standby: take over as master (1.1)
	msgPeers   = "peers"   // master → agent: the other nodes to check (ignored before 1.2)
)

// frame is one message: a type and its JSON body.
type frame struct {
	Type string          `json:"t"`
	Data json.RawMessage `json:"d,omitempty"`
}

// writeFrame writes a 4-byte big-endian length and the JSON frame.
func writeFrame(w io.Writer, typ string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b, err := json.Marshal(frame{Type: typ, Data: body})
	if err != nil {
		return err
	}
	buf := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	_, err = w.Write(buf)
	return err
}

// readFrame reads one frame no larger than max bytes.
func readFrame(r io.Reader, limit int) (frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if int64(n) > int64(limit) {
		return frame{}, fmt.Errorf("message of %d bytes exceeds the limit of %d", n, limit)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return frame{}, err
	}
	var f frame
	if err := json.Unmarshal(b, &f); err != nil {
		return frame{}, fmt.Errorf("malformed message: %w", err)
	}
	return f, nil
}

// Hello opens a session.
type Hello struct {
	Proto    string    `json:"proto"`
	Node     string    `json:"node"`
	Version  string    `json:"version"`
	Platform string    `json:"platform"`
	Time     time.Time `json:"time"`
	// Term is the highest master term this agent has seen; a master with
	// a lower one has been replaced and must step down.
	Term uint64 `json:"term,omitempty"`
	// Caps are what this node can do ("check:HTTP"...). Nodes from before
	// it was sent can do legacyCaps.
	Caps []string `json:"caps,omitempty"`
	// Listen is where the other nodes can reach this one (host:port; an
	// unspecified host means the address it connects from).
	Listen          string `json:"listen,omitempty"`
	PeerFingerprint string `json:"peer_fingerprint,omitempty"`
	// NeedState: this server joined but has no copy of the master's state
	// yet, so it checks nothing. The master sends it one at once instead
	// of at the next periodic sync, and counts it online only once it has.
	NeedState bool `json:"need_state,omitempty"`
}

// ReachPath answers peers checking this node: its name and which nodes
// it reaches itself. It carries no secrets.
const ReachPath = "/api/v1/agent/reach"

// Peers lists the nodes an agent checks besides its master.
type Peers struct {
	MasterFingerprint string     `json:"master_fingerprint,omitempty"`
	Master            string     `json:"master"` // the master's node name
	Nodes             []PeerAddr `json:"nodes"`
}

// PeerAddr is one node and where it listens.
type PeerAddr struct {
	Name        string `json:"name"`
	Addr        string `json:"addr"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// ReachView is a node's answer on ReachPath.
type ReachView struct {
	Node  string          `json:"node"`
	Reach map[string]bool `json:"reach"`
	// Term and Role let peers detect a foreign master (concurrent promote).
	// Role is "master" or "replica"; empty on older nodes.
	Term uint64 `json:"term,omitempty"`
	Role string `json:"role,omitempty"`
}

// CapReplica: the node is a full server keeping a copy of the master's
// state (a replica), not a probe-only agent.
const CapReplica = "replica"

// legacyCaps are the capabilities of nodes that do not announce any: the
// check types every release before 1.0 had.
var legacyCaps = []string{"check:HTTP", "check:TCP", "check:PING", "check:UDP", "check:EXEC", "check:DOCKER"}

// LocalCaps are this build's capabilities.
func LocalCaps() []string {
	out := make([]string, 0, len(probe.CheckTypes))
	for _, t := range probe.CheckTypes {
		out = append(out, "check:"+t)
	}
	return out
}

// Welcome accepts a session.
type Welcome struct {
	Proto string        `json:"proto"`
	Node  string        `json:"node"`
	Time  time.Time     `json:"time"`
	Ping  time.Duration `json:"ping"` // the master pings this often
	// Term counts master handovers: an agent refuses a master whose term
	// is lower than one it already served.
	Term uint64 `json:"term,omitempty"`
}

// Promote tells a standby to take over as master with this term.
type Promote struct {
	Term uint64 `json:"term"`
}

// ErrorMsg explains why a peer closes the session.
type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Servers, on codeSuperseded, are the addresses of the master that
	// replaced this one, when it is known.
	Servers []string `json:"servers,omitempty"`
}

// codeSuperseded: this master was replaced; try the next address.
const codeSuperseded = "superseded"

// AgentConfig is what one agent needs to probe: only the monitors it takes
// part in, with references already resolved. It never reaches the agent's
// disk.
type AgentConfig struct {
	Serial      uint32              `json:"serial"`
	Hash        string              `json:"hash"`
	Monitors    []AgentMonitor      `json:"monitors"`
	Anchors     []engine.AnchorSpec `json:"anchors,omitempty"`
	LocalDetect bool                `json:"local_detect"`
	Tunables    map[string]string   `json:"tunables,omitempty"`
	// Masters are the addresses an agent may find the master at: the
	// standbys that can take over. The agent keeps the one it reached.
	Masters []string `json:"masters,omitempty"`
	// Standby: this node keeps a copy of the master's state.
	Standby bool `json:"standby,omitempty"`
}

// AgentMonitor is one monitor as an agent runs it.
type AgentMonitor struct {
	Name        string         `json:"name"`
	Probe       probe.Spec     `json:"probe"`
	Scope       netscope.Scope `json:"scope,omitempty"`
	DefHash     string         `json:"def_hash"`
	SecretError string         `json:"secret_error,omitempty"`
}

// Status is an agent's report of its configuration and network.
type Status struct {
	Serial uint32         `json:"serial"`
	Hash   string         `json:"hash"`
	Time   time.Time      `json:"time"`
	Health *engine.Health `json:"health,omitempty"`
	// Scripts are this node's check scripts.
	Scripts []probe.Script `json:"scripts,omitempty"`
	// Reach is which other nodes this one can reach; Gateway how its last
	// notifications went out.
	Reach   map[string]bool       `json:"reach,omitempty"`
	Gateway []engine.GatewayState `json:"gateway,omitempty"`
	// Synced tells whether the node has its copy of the master's state
	// (nil: the node does not keep one, or predates this field).
	Synced *bool `json:"synced,omitempty"`
}

// Ping measures round-trip time and clock skew.
type Ping struct {
	Seq  uint64    `json:"seq"`
	Time time.Time `json:"time"`
}

// MergePath is where a node sends what it wrote while it checked on its
// own (GET: the master's marks for it; POST: a store.MergeBatch).
const MergePath = "/api/v1/agent/merge"

// DivergedPath is where a replaced master sends the configuration it kept
// changing after the copy (kept aside on the master, never applied).
const DivergedPath = "/api/v1/agent/diverged"
