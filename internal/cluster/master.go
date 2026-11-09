package cluster

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Sink receives what agents report; the engine implements it.
type Sink interface {
	NodeState(engine.NodeStatus)
	RemoteVote(engine.RemoteVote)
}

// MasterOptions configure the master end of the agent protocol.
type MasterOptions struct {
	// Auth resolves an agent token to the node it was issued for; addr is
	// recorded for the node list.
	Auth func(token, addr string) (string, error)
	// Known reports whether a node is declared in the configuration.
	Known func(node string) bool
	// Tunables returns the current tunables (ping interval, message size).
	Tunables func() tunables.Set
	Log      *slog.Logger
	Now      func() time.Time
	// Term is this master's term (0 before any handover).
	Term func() uint64
	// Superseded is told when an agent has served a newer master: this
	// one was replaced and must stop.
	Superseded func(term uint64, node string)
	// Retired: this master handed over or was replaced; agents are sent
	// on to the next address.
	Retired func() bool
	// Successor is the address of the master that replaced this one ("" =
	// unknown); agents turned away are sent there.
	Successor func() string
	// Standby reports whether a node may fetch the master's state.
	Standby func(node string) bool
	// WriteState writes the state archive for a standby, encrypted with
	// the passphrase it returns first.
	WriteState func(w http.ResponseWriter) error
	// Self is the master's own node name (for the peer lists).
	Self            func() string
	PeerFingerprint func() string
}

// Master accepts agent connections, forwards probe requests and configs
// to them and hands their votes and status to the engine. It implements
// engine.Remote.
type Master struct {
	o MasterOptions

	mu         sync.Mutex
	sink       Sink
	peers      map[string]*peer
	configs    map[string]*AgentConfig
	identities map[string]string
	listens    map[string]string // last address each node listens at
	closed     bool
	wg         sync.WaitGroup
}

// NewMaster creates the master end; SetSink must be called before agents
// connect.
func NewMaster(o MasterOptions) *Master {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Term == nil {
		o.Term = func() uint64 { return 0 }
	}
	if o.Tunables == nil {
		o.Tunables = func() tunables.Set { ts, _ := tunables.New(nil); return ts }
	}
	return &Master{o: o, peers: map[string]*peer{}, configs: map[string]*AgentConfig{}, identities: map[string]string{}, listens: map[string]string{}}
}

// SetSink sets where agent reports go.
func (m *Master) SetSink(s Sink) {
	m.mu.Lock()
	m.sink = s
	m.mu.Unlock()
}

func (m *Master) getSink() Sink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink
}

// Probe asks a connected agent to run a round. It never blocks: a node that
// is not connected, or whose send queue is full, reports false.
func (m *Master) Probe(node string, req engine.ProbeRequest) bool {
	m.mu.Lock()
	p := m.peers[node]
	m.mu.Unlock()
	return p != nil && p.send(msgProbe, req)
}

// SetConfigs replaces the per-node agent configurations and sends the ones
// that changed. Agents whose node is no longer configured are disconnected.
func (m *Master) SetConfigs(cfgs map[string]*AgentConfig) {
	m.mu.Lock()
	m.configs = cfgs
	peers := make([]*peer, 0, len(m.peers))
	for _, p := range m.peers {
		peers = append(peers, p)
	}
	m.mu.Unlock()
	for _, p := range peers {
		if cfg := cfgs[p.node]; cfg != nil {
			p.setConfig(cfg)
		} else if !m.o.Known(p.node) {
			p.fail("unknown_node", "node "+p.node+" was removed from the configuration")
		}
	}
}

// Connected lists the nodes with a live session.
func (m *Master) Connected() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.peers))
	for n := range m.peers {
		out = append(out, n)
	}
	return out
}

// Disconnect closes a node's session (after its token was revoked).
func (m *Master) Disconnect(node, why string) {
	m.mu.Lock()
	p := m.peers[node]
	m.mu.Unlock()
	if p != nil {
		p.fail("revoked", why)
	}
}

// Retire ends every session telling the agents this master was replaced;
// they move on to the next address and are refused here (Retired).
func (m *Master) Retire(why string) {
	m.mu.Lock()
	peers := make([]*peer, 0, len(m.peers))
	for _, p := range m.peers {
		peers = append(peers, p)
	}
	m.mu.Unlock()
	for _, p := range peers {
		p.log.Info("closing agent session", "reason", why)
		p.send(msgError, ErrorMsg{Code: codeSuperseded, Message: why, Servers: m.successor()})
		time.AfterFunc(time.Second, p.close)
	}
}

func (m *Master) successor() []string {
	if m.o.Successor == nil {
		return nil
	}
	if s := m.o.Successor(); s != "" {
		return []string{s}
	}
	return nil
}

// NotifyState asks a connected standby to fetch the state now.
func (m *Master) NotifyState(node string) bool {
	m.mu.Lock()
	p := m.peers[node]
	m.mu.Unlock()
	return p != nil && p.send(msgState, struct{}{})
}

// Promote tells a connected standby to take over with term.
func (m *Master) Promote(node string, term uint64) bool {
	m.mu.Lock()
	p := m.peers[node]
	m.mu.Unlock()
	return p != nil && p.send(msgPromote, Promote{Term: term})
}

// ServeState sends the master's state to an authenticated standby.
func (m *Master) ServeState(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "agents must connect with TLS", http.StatusForbidden)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "missing agent token", http.StatusUnauthorized)
		return
	}
	node, err := m.o.Auth(token, r.RemoteAddr)
	if err != nil {
		http.Error(w, "invalid agent token", http.StatusUnauthorized)
		return
	}
	if m.o.Standby == nil || m.o.WriteState == nil || !m.o.Standby(node) {
		http.Error(w, "node "+node+" is not a standby", http.StatusForbidden)
		return
	}
	w.Header().Set(StateTermHeader, fmt.Sprint(m.o.Term()))
	if err := m.o.WriteState(w); err != nil {
		m.o.Log.Error("state for standby", "node", node, "err", err)
		return
	}
	m.o.Log.Info("state sent to standby", "node", node)
}

// Close ends every session and waits for them.
func (m *Master) Close() {
	m.mu.Lock()
	m.closed = true
	peers := make([]*peer, 0, len(m.peers))
	for _, p := range m.peers {
		peers = append(peers, p)
	}
	m.mu.Unlock()
	for _, p := range peers {
		p.close()
	}
	m.wg.Wait()
}

// ServeHTTP upgrades an authenticated agent request to a session. Agents
// must use TLS: the session carries resolved secrets.
func (m *Master) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "agents must connect with TLS", http.StatusForbidden)
		return
	}
	if up := r.Header.Get("Upgrade"); !headerHas(r.Header, "Connection", "upgrade") || !strings.EqualFold(up, Upgrade) {
		w.Header().Set("Upgrade", Upgrade)
		http.Error(w, "upgrade to "+Upgrade+" required", http.StatusUpgradeRequired)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "missing agent token", http.StatusUnauthorized)
		return
	}
	node, err := m.o.Auth(token, r.RemoteAddr)
	if err != nil {
		http.Error(w, "invalid agent token", http.StatusUnauthorized)
		return
	}
	want := r.Header.Get(NodeHeader)
	if want != "" && want != node {
		http.Error(w, fmt.Sprintf("this token belongs to node %s, not %s", node, want), http.StatusForbidden)
		return
	}
	if !m.o.Known(node) && !(m.o.Retired != nil && m.o.Retired()) {
		http.Error(w, "node "+node+" is not declared in the configuration", http.StatusForbidden)
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be upgraded", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Time{})
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: %s\r\nConnection: Upgrade\r\n\r\n", r.Header.Get("Upgrade"))
	if err := brw.Flush(); err != nil {
		return
	}
	m.session(node, token, conn, brw.Reader, r.RemoteAddr)
}

func headerHas(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func (m *Master) limits() (ping time.Duration, maxMsg int) {
	ts := m.o.Tunables()
	return ts.Duration("cluster.ping-interval"), ts.Int("cluster.max-message")
}

// session runs one agent connection until it ends.
func (m *Master) session(node, token string, conn net.Conn, r *bufio.Reader, addr string) {
	ping, maxMsg := m.limits()
	log := m.o.Log.With("node", node, "addr", addr)
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	f, err := readFrame(r, maxMsg)
	if err != nil || f.Type != msgHello {
		log.Warn("agent handshake failed", "err", err, "type", f.Type)
		return
	}
	var h Hello
	if err := json.Unmarshal(f.Data, &h); err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if !compatible(h.Proto) {
		_ = writeFrame(conn, msgError, ErrorMsg{Code: "version", Message: fmt.Sprintf("protocol %s is not compatible with the master's %s; upgrade unagikuma on this node", h.Proto, ProtoVersion)})
		log.Warn("agent protocol not compatible", "proto", h.Proto, "version", h.Version)
		return
	}
	if m.o.Retired != nil && m.o.Retired() {
		_ = writeFrame(conn, msgError, ErrorMsg{Code: codeSuperseded, Message: "this master handed over to a newer one", Servers: m.successor()})
		return
	}
	if term := m.o.Term(); h.Term > term {
		_ = writeFrame(conn, msgError, ErrorMsg{Code: codeSuperseded, Message: fmt.Sprintf("this master (term %d) was replaced by a newer one (term %d)", term, h.Term)})
		log.Warn("an agent has served a newer master: this one was replaced", "term", term, "newer", h.Term)
		if m.o.Superseded != nil {
			m.o.Superseded(h.Term, node)
		}
		return
	}
	now := m.o.Now()
	if err := writeFrame(conn, msgWelcome, Welcome{Proto: ProtoVersion, Node: node, Time: now, Ping: ping, Term: m.o.Term()}); err != nil {
		return
	}
	p := &peer{
		m: m, node: node, token: token, addr: addr, conn: conn, log: log, ping: ping,
		out: make(chan outFrame, sendQueue), cfgReady: make(chan struct{}, 1), closed: make(chan struct{}),
		pings: map[uint64]time.Time{}, caps: capSet(h.Caps),
		status: engine.NodeStatus{Node: node, Online: !h.NeedState, Since: now, LastSeen: now, Skew: h.Time.Sub(now),
			Version: h.Version, Platform: h.Platform, Addr: addr, Listen: listenAddr(h.Listen, addr), PeerFingerprint: h.PeerFingerprint},
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	old := m.peers[node]
	m.peers[node] = p
	if l := p.status.Listen; l != "" {
		m.listens[node] = l
		if h.PeerFingerprint != "" {
			m.identities[node] = h.PeerFingerprint
		}
	}
	cfg := m.configs[node]
	m.mu.Unlock()
	if old != nil {
		old.fail("replaced", "a newer connection of the same node replaced this one")
	}
	log.Info("agent connected", "version", h.Version, "platform", h.Platform)
	if cfg != nil {
		p.setConfig(cfg)
	}
	if h.NeedState && m.o.Standby != nil && m.o.Standby(node) {
		// A new member checks nothing until it has the state: send it now.
		p.send(msgState, struct{}{})
	}
	go p.writer()
	go p.pinger()
	p.report()
	m.sendPeers()
	err = p.reader(r, maxMsg)
	p.close()
	m.mu.Lock()
	current := m.peers[node] == p
	if current {
		delete(m.peers, node)
	}
	m.mu.Unlock()
	if current {
		m.sendPeers()
		st := p.snapshot()
		st.Online, st.Since = false, m.o.Now()
		if s := m.getSink(); s != nil {
			s.NodeState(st)
		}
	}
	log.Info("agent disconnected", "err", err)
}

const (
	handshakeTimeout = 10 * time.Second
	sendQueue        = 1024
)

type outFrame struct {
	typ string
	v   any
}

// peer is one agent session on the master.
type peer struct {
	m     *Master
	node  string
	token string
	addr  string
	conn  net.Conn
	log   *slog.Logger
	ping  time.Duration

	out      chan outFrame
	cfgReady chan struct{}
	closed   chan struct{}
	once     sync.Once

	mu       sync.Mutex
	status   engine.NodeStatus
	caps     map[string]bool
	pending  *AgentConfig // latest config not yet written
	sentHash string       // hash of the last config queued
	seq      uint64
	pings    map[uint64]time.Time
}

func (p *peer) send(typ string, v any) bool {
	select {
	case <-p.closed:
		return false
	default:
	}
	select {
	case p.out <- outFrame{typ, v}:
		return true
	default:
		return false
	}
}

// setConfig queues cfg; only the newest pending config is written.
func (p *peer) setConfig(cfg *AgentConfig) {
	p.mu.Lock()
	if cfg.Hash != "" && cfg.Hash == p.sentHash {
		p.mu.Unlock()
		return // unchanged (a re-apply that did not touch this node)
	}
	hash := cfg.Hash
	cfg, skipped := p.supported(cfg)
	p.pending, p.sentHash = cfg, hash
	p.status.Unsupported = skipped
	p.mu.Unlock()
	if len(skipped) > 0 {
		p.log.Warn("node cannot run some checks; upgrade unagikuma there", "monitors", skipped)
	}
	select {
	case p.cfgReady <- struct{}{}:
	default:
	}
}

// capSet reads announced capabilities (none: an older node).
func capSet(caps []string) map[string]bool {
	if len(caps) == 0 {
		caps = legacyCaps
	}
	m := make(map[string]bool, len(caps))
	for _, c := range caps {
		m[c] = true
	}
	return m
}

// supported drops the monitors whose check type the node lacks; it
// abstains on them like a node that cannot reach the target.
func (p *peer) supported(cfg *AgentConfig) (*AgentConfig, []string) {
	var keep []AgentMonitor
	var skipped []string
	for _, m := range cfg.Monitors {
		if p.caps["check:"+strings.ToUpper(m.Probe.Type)] {
			keep = append(keep, m)
		} else {
			skipped = append(skipped, m.Name)
		}
	}
	if skipped == nil {
		return cfg, nil
	}
	c := *cfg
	c.Monitors = keep
	return &c, skipped
}

func (p *peer) fail(code, msg string) {
	p.log.Info("closing agent session", "reason", msg)
	p.send(msgError, ErrorMsg{Code: code, Message: msg})
	// Give the writer a moment to deliver the reason before closing.
	time.AfterFunc(time.Second, p.close)
}

func (p *peer) close() {
	p.once.Do(func() {
		close(p.closed)
		p.conn.Close()
	})
}

func (p *peer) snapshot() engine.NodeStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *peer) report() {
	if s := p.m.getSink(); s != nil {
		s.NodeState(p.snapshot())
	}
}

func (p *peer) write(typ string, v any) error {
	_ = p.conn.SetWriteDeadline(time.Now().Add(3 * p.ping))
	return writeFrame(p.conn, typ, v)
}

// writer serialises outgoing frames. A pending configuration goes first so
// probe requests for a new definition do not overtake it.
func (p *peer) writer() {
	for {
		select {
		case <-p.cfgReady:
			if !p.writeConfig() {
				return
			}
			continue
		default:
		}
		select {
		case <-p.closed:
			return
		case <-p.cfgReady:
			if !p.writeConfig() {
				return
			}
		case f := <-p.out:
			if err := p.write(f.typ, f.v); err != nil {
				p.close()
				return
			}
		}
	}
}

func (p *peer) writeConfig() bool {
	p.mu.Lock()
	cfg := p.pending
	p.pending = nil
	p.mu.Unlock()
	if cfg == nil {
		return true
	}
	if err := p.write(msgConfig, cfg); err != nil {
		p.close()
		return false
	}
	return true
}

// pinger measures the session: round-trip time and clock skew, and
// silence that means the connection is dead. It also re-checks the token,
// which may have been revoked by another process sharing the database.
func (p *peer) pinger() {
	t := time.NewTicker(p.ping)
	defer t.Stop()
	for first := true; ; first = false {
		if !first {
			if node, err := p.m.o.Auth(p.token, p.addr); err != nil || node != p.node {
				p.fail("revoked", "the token of node "+p.node+" is no longer valid")
				return
			}
		}
		p.mu.Lock()
		p.seq++
		seq := p.seq
		now := time.Now()
		p.pings[seq] = now
		for s, at := range p.pings {
			if now.Sub(at) > 3*p.ping {
				delete(p.pings, s)
			}
		}
		p.mu.Unlock()
		p.send(msgPing, Ping{Seq: seq, Time: p.m.o.Now()})
		select {
		case <-p.closed:
			return
		case <-t.C:
		}
	}
}

// reader handles agent messages. An agent can only report: votes, its
// status and ping answers. Anything else ends the session.
func (p *peer) reader(r *bufio.Reader, maxMsg int) error {
	for {
		_ = p.conn.SetReadDeadline(time.Now().Add(3 * p.ping))
		f, err := readFrame(r, maxMsg)
		if err != nil {
			return err
		}
		now := p.m.o.Now()
		p.mu.Lock()
		p.status.LastSeen = now
		p.mu.Unlock()
		switch f.Type {
		case msgVote:
			var v engine.RemoteVote
			if err := json.Unmarshal(f.Data, &v); err != nil {
				return err
			}
			v.Node = p.node // an agent votes only for itself
			if s := p.m.getSink(); s != nil {
				s.RemoteVote(v)
			}
		case msgStatus:
			var st Status
			if err := json.Unmarshal(f.Data, &st); err != nil {
				return err
			}
			p.mu.Lock()
			p.status.Serial, p.status.Health, p.status.Scripts = st.Serial, st.Health, st.Scripts
			p.status.Reach, p.status.Gateway = st.Reach, st.Gateway
			if st.Synced != nil && *st.Synced && !p.status.Online {
				// The new member has the state and checks from now on.
				p.status.Online, p.status.Since = true, now
			}
			p.mu.Unlock()
			p.report()
		case msgPong:
			var pg Ping
			if err := json.Unmarshal(f.Data, &pg); err != nil {
				return err
			}
			got := time.Now()
			p.mu.Lock()
			sent, ok := p.pings[pg.Seq]
			delete(p.pings, pg.Seq)
			if ok {
				rtt := got.Sub(sent)
				p.status.RTT = rtt
				// The agent read its clock about half a round trip after
				// the ping left.
				p.status.Skew = pg.Time.Sub(now.Add(-rtt / 2))
			}
			p.mu.Unlock()
			if ok {
				p.report()
			}
		case msgError:
			var e ErrorMsg
			_ = json.Unmarshal(f.Data, &e)
			return errors.New("agent: " + e.Message)
		default:
			return fmt.Errorf("unexpected %q message from agent", f.Type)
		}
	}
}

// HasCap reports whether a connected node announced a capability.
func (m *Master) HasCap(node, c string) bool {
	m.mu.Lock()
	p := m.peers[node]
	m.mu.Unlock()
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps[c]
}

// listenAddr completes an agent's announced listen address: an unspecified
// host is the address it connected from.
func listenAddr(listen, remote string) string {
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		if rh, _, err := net.SplitHostPort(remote); err == nil {
			host = rh
		}
	}
	return net.JoinHostPort(host, port)
}

// sendPeers tells every connected node where the others listen, so each
// can check them and report what it reaches.
func (m *Master) sendPeers() {
	self := ""
	if m.o.Self != nil {
		self = m.o.Self()
	}
	m.mu.Lock()
	peers := make([]*peer, 0, len(m.peers))
	for _, p := range m.peers {
		peers = append(peers, p)
	}
	var addrs []PeerAddr
	for n, l := range m.listens {
		pin := m.identities[n]
		addrs = append(addrs, PeerAddr{Name: n, Addr: l, Fingerprint: pin})
	}
	m.mu.Unlock()
	// Nodes removed from the configuration are not checked any more.
	keep := addrs[:0]
	for _, a := range addrs {
		if m.o.Known == nil || m.o.Known(a.Name) {
			keep = append(keep, a)
		}
	}
	addrs = keep
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Name < addrs[j].Name })
	for _, p := range peers {
		var others []PeerAddr
		for _, a := range addrs {
			if a.Name != p.node {
				others = append(others, a)
			}
		}
		pin := ""
		if m.o.PeerFingerprint != nil {
			pin = m.o.PeerFingerprint()
		}
		p.send(msgPeers, Peers{Master: self, MasterFingerprint: pin, Nodes: others})
	}
}

// Reach is the master's own view: the configured nodes it has a session
// with.
func (m *Master) Reach(nodes []string) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		out[n] = m.peers[n] != nil
	}
	return out
}

// PeerListens returns host:port where each known agent last announced it listens.
func (m *Master) PeerListens() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.listens))
	for _, l := range m.listens {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// PeerIdentities are identities learned through token-authenticated sessions.
func (m *Master) PeerIdentities() []PeerAddr {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PeerAddr
	for n, pin := range m.identities {
		if m.listens[n] != "" && pin != "" {
			out = append(out, PeerAddr{Name: n, Addr: m.listens[n], Fingerprint: pin})
		}
	}
	return out
}
