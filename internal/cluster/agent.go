package cluster

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// AgentOptions configure a probe agent.
type AgentOptions struct {
	// PeerFingerprint is this listening server's CA, announced only over the authenticated master session.
	PeerFingerprint string
	InitialPeers    []PeerAddr
	// Server is the master's URL, e.g. https://master:8077.
	Server string
	// Node is the expected node name; empty accepts the token's node.
	Node  string
	Token string
	// Fingerprint pins the master's cluster CA ("sha256:…").
	Fingerprint string
	// Insecure accepts any master certificate (not recommended).
	Insecure bool
	Version  string
	Log      *slog.Logger
	// Probe runs a check (default probe.Run).
	Probe func(context.Context, probe.Spec) probe.Result
	// Engine adjusts the dependencies of the agent's local engine (tests).
	Engine func(*engine.Deps)
	// MaxBackoff caps the wait between reconnects (default 1 minute).
	MaxBackoff time.Duration
	// Emergency is told when the master stays unreachable longer than
	// cluster.emergency-after, and again when it is back: the master
	// cannot report its own outage.
	Emergency *notify.Channel
	// Send delivers emergency messages (default notify.Sender).
	Send func(context.Context, notify.Channel, *notify.Message) notify.Attempt
	// Lang is the language of emergency messages.
	Lang string
	// EmergencyAfter overrides cluster.emergency-after (tests).
	EmergencyAfter time.Duration
	// Servers are more addresses of the master (standbys that can take
	// over); the agent moves on to the next when one is unreachable or
	// was replaced.
	Servers []string
	// Term is the highest master term seen before; a master with a lower
	// term was replaced and is not served.
	Term uint64
	// OnTerm and OnMasters are told when the term or the address list
	// changes, to keep them across restarts.
	OnTerm    func(term uint64)
	OnMasters func(servers []string)
	// OnState is told to fetch the master's state (standbys); OnPromote
	// to take over as master.
	OnState   func()
	OnPromote func(term uint64)
	// Synced reports whether this node has its copy of the master's
	// state; nil means it keeps none and is ready as soon as it connects.
	Synced func() bool
	// Replica: this node is a full server that keeps a copy of the
	// master's state and checks on its own while the master is away.
	Replica bool
	// Listen is where peers can reach this node (host:port; "" for an
	// agent that does not listen).
	Listen string
	// Gateway reports how this node's last notifications went out.
	Gateway func() []engine.GatewayState
}

// staleError: the master reached was replaced by a newer one.
type staleError struct{ Msg string }

func (e *staleError) Error() string { return e.Msg }

// AuthError is a refusal by the master that retrying will not fix.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// Agent is a running probe agent.
type Agent struct {
	o   AgentOptions
	log *slog.Logger
	pin string
	eng *engine.Engine

	smu     sync.Mutex
	servers []string // master URLs, the one in use first after a failover
	cur     int
	term    uint64

	mu       sync.Mutex
	cfg      *AgentConfig
	monitors map[string]AgentMonitor
	ts       tunables.Set
	sem      chan struct{}
	conn     AgentStatus

	peers      peerSet
	peersKick  chan struct{}
	sessMu     sync.Mutex
	sessCancel context.CancelFunc
	live       *agentSession // the live session, for PushStatus
}

// AgentStatus is the agent's view of its connection, for its local UI.
type AgentStatus struct {
	Server    string    `json:"server"`
	Node      string    `json:"node"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since"`
	LastError string    `json:"last_error,omitempty"`
	Serial    uint32    `json:"serial,omitempty"`
	Monitors  int       `json:"monitors"`
}

// Status reports the connection state.
func (a *Agent) Status() AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.conn
	st.Server, st.Monitors = a.Server(), len(a.monitors)
	if st.Node == "" {
		st.Node = a.o.Node
	}
	if a.cfg != nil {
		st.Serial = a.cfg.Serial
	}
	return st
}

func (a *Agent) setConnected(on bool, node string, err error) {
	a.mu.Lock()
	was := a.conn.Connected
	if a.conn.Connected != on || a.conn.Since.IsZero() {
		a.conn.Since = time.Now()
	}
	a.conn.Connected = on
	if node != "" {
		a.conn.Node = node
	}
	if err != nil {
		a.conn.LastError = err.Error()
	} else if on {
		a.conn.LastError = ""
	}
	kick := a.peersKick
	a.mu.Unlock()
	if on && !was && kick != nil {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
}

// NewAgent validates the options.
func NewAgent(o AgentOptions) (*Agent, error) {
	if o.Token == "" {
		return nil, errors.New("an agent token is required")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Probe == nil {
		o.Probe = probe.Run
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 15 * time.Second
	}
	a := &Agent{o: o, log: o.Log, monitors: map[string]AgentMonitor{}, term: o.Term, peersKick: make(chan struct{}, 1)}
	for _, s := range append([]string{o.Server}, o.Servers...) {
		u, err := NormalizeServer(s)
		if err != nil {
			return nil, err
		}
		a.addServer(u)
	}
	var err error
	if !o.Insecure {
		if o.Fingerprint == "" {
			return nil, errors.New("--ca-fingerprint is required (it is part of the install command shown by the master)")
		}
		if a.pin, err = normalizeFingerprint(o.Fingerprint); err != nil {
			return nil, err
		}
	}
	a.ts, _ = tunables.New(nil)
	a.sem = make(chan struct{}, a.ts.Int("probe.max-concurrency"))
	deps := engine.Deps{
		Probe: o.Probe,
		Send: func(context.Context, notify.Channel, *notify.Message) notify.Attempt {
			return notify.Attempt{Error: "agents do not send notifications"}
		},
		Sink: newAgentSink(o.Log),
		Log:  o.Log,
	}
	if o.Engine != nil {
		o.Engine(&deps)
	}
	a.eng = engine.New(deps)
	a.eng.Apply(engine.Config{Tunables: a.ts, LocalDetect: true})
	return a, nil
}

// Run connects to the master and serves it until ctx ends, reconnecting
// with backoff. While disconnected the agent does not probe: without a
// round from the master a vote would count for nothing.
func (a *Agent) Run(ctx context.Context) error {
	if len(a.o.InitialPeers) > 0 {
		a.setPeers(Peers{Nodes: a.o.InitialPeers})
	}
	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { a.eng.Run(ectx); close(done) }()
	defer func() { cancel(); <-done }()

	if a.o.Emergency != nil {
		go a.watchMaster(ctx)
	}
	go a.checkPeers(ectx)
	a.setConnected(false, "", nil)
	backoff := time.Second
	for {
		start := time.Now()
		err := a.session(ctx)
		a.setConnected(false, "", err)
		if ctx.Err() != nil {
			return nil
		}
		var ae *AuthError
		var se *staleError
		switch {
		case errors.As(err, &se):
			a.log.Warn("master was replaced", "err", err)
			a.next()
			backoff = time.Second
		case errors.As(err, &ae):
			a.log.Error("master refused this agent", "err", err)
			backoff = a.o.MaxBackoff
		case time.Since(start) > a.o.MaxBackoff:
			backoff = time.Second // the session was healthy for a while
			a.log.Warn("disconnected from master", "err", err)
		default:
			a.log.Warn("cannot reach master", "err", err)
			a.next()
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, a.o.MaxBackoff)
	}
}

func (a *Agent) dial(ctx context.Context) (*tls.Conn, error) {
	addr, _ := hostPort(a.Server())
	return a.dialAddr(ctx, addr)
}

func (a *Agent) dialAddr(ctx context.Context, addr string) (*tls.Conn, error) {
	d := net.Dialer{Timeout: handshakeTimeout, KeepAlive: 30 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conf := &tls.Config{
		ServerName: AgentServerName,
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
		// Verification is done by VerifyConnection against the pinned CA,
		// not the system roots: the master's CA is its own.
		InsecureSkipVerify: true,
	}
	if !a.o.Insecure {
		conf.VerifyConnection = pinnedVerifier(a.pin)
	}
	c := tls.Client(raw, conf)
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := c.HandshakeContext(hctx); err != nil {
		raw.Close()
		return nil, err
	}
	return c, nil
}

// session runs one connection to the master.
func (a *Agent) session(ctx context.Context) error {
	conn, err := a.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	sctx, cancel := context.WithCancel(ctx)
	a.sessMu.Lock()
	a.sessCancel = cancel
	a.sessMu.Unlock()
	defer func() {
		cancel()
		a.sessMu.Lock()
		if a.sessCancel != nil {
			// only clear if we still own it
			a.sessCancel = nil
		}
		a.sessMu.Unlock()
	}()
	go func() { <-sctx.Done(); conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	addr, _ := hostPort(a.Server())
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: ConnectPath}, Host: addr, Header: http.Header{}}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", Upgrade)
	req.Header.Set("Authorization", "Bearer "+a.o.Token)
	if a.o.Node != "" {
		req.Header.Set(NodeHeader, a.o.Node)
	}
	req.Header.Set("User-Agent", "unagikuma-agent/"+a.o.Version)
	if err := req.Write(conn); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		msg := fmt.Sprintf("master answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return &AuthError{msg}
		}
		return errors.New(msg)
	}
	a.mu.Lock()
	maxMsg := a.ts.Int("cluster.max-message")
	a.mu.Unlock()
	if err := writeFrame(conn, msgHello, Hello{Proto: ProtoVersion, Node: a.o.Node, Version: a.o.Version,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, Time: time.Now(), Term: a.Term(), Caps: a.caps(), Listen: a.o.Listen, PeerFingerprint: a.o.PeerFingerprint,
		NeedState: a.o.Synced != nil && !a.o.Synced()}); err != nil {
		return err
	}
	f, err := readFrame(br, maxMsg)
	if err != nil {
		return err
	}
	if f.Type == msgError {
		var e ErrorMsg
		_ = json.Unmarshal(f.Data, &e)
		if e.Code == codeSuperseded {
			a.follow(e.Servers)
			return &staleError{e.Message}
		}
		return &AuthError{e.Message}
	}
	var w Welcome
	if f.Type != msgWelcome || json.Unmarshal(f.Data, &w) != nil || w.Ping <= 0 {
		return fmt.Errorf("unexpected %q message from master", f.Type)
	}
	if known := a.Term(); w.Term < known {
		return &staleError{fmt.Sprintf("master at %s has term %d, older than %d: it was replaced", addr, w.Term, known)}
	} else if w.Term > known {
		a.smu.Lock()
		a.term = w.Term
		a.smu.Unlock()
		if a.o.OnTerm != nil {
			a.o.OnTerm(w.Term)
		}
	}
	_ = conn.SetDeadline(time.Time{})
	a.log.Info("connected to master", "node", w.Node, "server", addr, "proto", w.Proto, "term", w.Term)
	a.setConnected(true, w.Node, nil)
	// Keep membership.server in sync when we moved on via next() (BUG-8).
	if a.o.OnMasters != nil {
		a.o.OnMasters(a.Servers())
	}

	s := &agentSession{a: a, conn: conn, ping: w.Ping, out: make(chan outFrame, sendQueue), done: make(chan struct{}), dead: make(chan struct{})}
	go s.writer()
	a.sessMu.Lock()
	a.live = s
	a.sessMu.Unlock()
	defer func() {
		a.sessMu.Lock()
		if a.live == s {
			a.live = nil
		}
		a.sessMu.Unlock()
		close(s.done)
	}()
	return s.reader(sctx, br)
}

// agentSession is one connection's state on the agent.
type agentSession struct {
	a    *Agent
	conn net.Conn
	ping time.Duration
	out  chan outFrame
	done chan struct{} // the session ended
	dead chan struct{} // the writer failed; nothing more can be sent
}

// send queues a frame. It gives up once the session or its writer is
// gone, so a full queue can never wedge the reader.
func (s *agentSession) send(typ string, v any) {
	select {
	case s.out <- outFrame{typ, v}:
	case <-s.done:
	case <-s.dead:
	}
}

func (s *agentSession) writer() {
	defer close(s.dead)
	for {
		select {
		case <-s.done:
			return
		case f := <-s.out:
			_ = s.conn.SetWriteDeadline(time.Now().Add(3 * s.ping))
			if err := writeFrame(s.conn, f.typ, f.v); err != nil {
				s.conn.Close() // also ends the reader
				return
			}
		}
	}
}

func (s *agentSession) reader(ctx context.Context, r *bufio.Reader) error {
	for {
		s.a.mu.Lock()
		maxMsg := s.a.ts.Int("cluster.max-message")
		s.a.mu.Unlock()
		_ = s.conn.SetReadDeadline(time.Now().Add(3 * s.ping))
		f, err := readFrame(r, maxMsg)
		if err != nil {
			return err
		}
		switch f.Type {
		case msgConfig:
			var cfg AgentConfig
			if err := json.Unmarshal(f.Data, &cfg); err != nil {
				return err
			}
			s.a.apply(&cfg)
			if cfg.Masters != nil {
				s.a.setMasters(cfg.Masters)
			}
			s.send(msgStatus, s.a.status())
		case msgProbe:
			var req engine.ProbeRequest
			if err := json.Unmarshal(f.Data, &req); err != nil {
				return err
			}
			go s.probe(ctx, req)
		case msgPing:
			var p Ping
			if err := json.Unmarshal(f.Data, &p); err != nil {
				return err
			}
			s.send(msgPong, Ping{Seq: p.Seq, Time: time.Now()})
			s.send(msgStatus, s.a.status())
		case msgError:
			var e ErrorMsg
			_ = json.Unmarshal(f.Data, &e)
			if e.Code == "revoked" || e.Code == "unknown_node" || e.Code == "version" {
				return &AuthError{e.Message}
			}
			if e.Code == codeSuperseded {
				s.a.follow(e.Servers)
				return &staleError{e.Message}
			}
			return errors.New("master: " + e.Message)
		case msgPeers:
			var p Peers
			if err := json.Unmarshal(f.Data, &p); err != nil {
				return err
			}
			s.a.setPeers(p)
		case msgState:
			if s.a.o.OnState != nil {
				go s.a.o.OnState()
			}
		case msgPromote:
			var p Promote
			if err := json.Unmarshal(f.Data, &p); err != nil {
				return err
			}
			if s.a.o.OnPromote != nil {
				go s.a.o.OnPromote(p.Term)
			}
		default:
			// Newer masters may send messages this agent does not know.
			s.a.log.Debug("ignoring message", "type", f.Type)
		}
	}
}

// probe runs one requested round and answers with this node's vote.
func (s *agentSession) probe(ctx context.Context, req engine.ProbeRequest) {
	a := s.a
	a.mu.Lock()
	m, ok := a.monitors[req.Monitor]
	// A new configuration may resize the limit; this probe keeps the
	// semaphore it started with.
	sem := a.sem
	a.mu.Unlock()
	v := engine.RemoteVote{Round: req.Round, Monitor: req.Monitor, DefHash: m.DefHash}
	switch {
	case !ok:
		v.Vote, v.Reason = probe.VoteAbstain, probe.ReasonConfig
	case m.DefHash != req.DefHash:
		// Not synced to this definition yet: the master counts it as no
		// answer, so do not spend a probe on it.
		v.Vote, v.Reason = probe.VoteAbstain, probe.ReasonConfig
	case m.SecretError != "":
		v.Vote, v.Reason = probe.VoteAbstain, probe.ReasonSecretUnavailable
		v.Result = probe.Result{Vote: probe.VoteAbstain, Reason: probe.ReasonSecretUnavailable, Detail: m.SecretError}
	default:
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		pctx, cancel := context.WithTimeout(ctx, m.Probe.Timeout+handshakeTimeout)
		av := a.eng.ProbeFor(pctx, m.Name, m.Probe, m.Scope)
		cancel()
		<-sem
		v.Result, v.Vote, v.Reason, v.Excluded = av.Result, av.Vote, av.Reason, av.Excluded
	}
	if ctx.Err() == nil {
		s.send(msgVote, v)
	}
}

// apply installs a configuration from the master.
func (a *Agent) apply(cfg *AgentConfig) {
	ts, err := tunables.New(cfg.Tunables)
	if err != nil {
		a.log.Warn("tunables from master", "err", err)
		ts, _ = tunables.New(nil)
	}
	mons := make(map[string]AgentMonitor, len(cfg.Monitors))
	keep := map[string]bool{}
	for _, m := range cfg.Monitors {
		mons[m.Name] = m
		keep[m.Name] = true
	}
	a.mu.Lock()
	a.cfg, a.monitors, a.ts = cfg, mons, ts
	if n := ts.Int("probe.max-concurrency"); n != cap(a.sem) {
		a.sem = make(chan struct{}, n)
	}
	a.mu.Unlock()
	a.eng.Apply(engine.Config{Serial: cfg.Serial, Tunables: ts, LocalDetect: cfg.LocalDetect, Anchors: cfg.Anchors})
	a.eng.ForgetAgentMonitors(keep)
	a.log.Info("configuration applied", "serial", cfg.Serial, "monitors", len(cfg.Monitors))
}

func (a *Agent) status() Status {
	a.mu.Lock()
	var st Status
	if a.cfg != nil {
		st.Serial, st.Hash = a.cfg.Serial, a.cfg.Hash
	}
	a.mu.Unlock()
	h := a.eng.Overview().Health
	st.Health, st.Time, st.Scripts = &h, time.Now(), probe.Scripts()
	st.Reach = a.Reach()
	if a.o.Synced != nil {
		ok := a.o.Synced()
		st.Synced = &ok
	}
	if a.o.Gateway != nil {
		st.Gateway = a.o.Gateway()
	}
	return st
}

// agentSink keeps the agent engine's bookkeeping in memory: an agent has no
// database, the master records everything that matters.
type agentSink struct {
	log *slog.Logger
	mu  sync.Mutex
	id  int64
}

func newAgentSink(log *slog.Logger) *agentSink { return &agentSink{log: log} }

func (s *agentSink) SaveState(engine.StateRecord)                     {}
func (s *agentSink) DeleteState(string)                               {}
func (s *agentSink) Verdict(engine.VerdictRecord)                     {}
func (s *agentSink) Evidence(string, string, time.Time, probe.Result) {}
func (s *agentSink) SaveIntent(*engine.Intent) error                  { return nil }
func (s *agentSink) Delivery(engine.DeliveryRecord)                   {}
func (s *agentSink) Counter(string, int)                              {}
func (s *agentSink) Event(e engine.Event) {
	s.log.Debug("local event", "type", e.Type, "message", e.Message)
}
func (s *agentSink) SaveIncident(i *engine.Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i.ID == 0 {
		s.id++
		i.ID = s.id
	}
	return nil
}

// watchMaster sends the emergency message once per outage of the master,
// and a recovery message when the agent is connected again.
func (a *Agent) watchMaster(ctx context.Context) {
	send := a.o.Send
	if send == nil {
		send = (&notify.Sender{Log: a.log, Instance: a.o.Node}).Send
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var alertedAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		st := a.Status()
		a.mu.Lock()
		after := a.ts.Duration("cluster.emergency-after")
		a.mu.Unlock()
		if a.o.EmergencyAfter > 0 {
			after = a.o.EmergencyAfter
		}
		now := time.Now()
		switch {
		case !st.Connected && alertedAt.IsZero() && now.Sub(st.Since) >= after:
			alertedAt = st.Since
			a.emergency(ctx, send, "sys.agent_master_down", st, now.Sub(st.Since))
		case st.Connected && !alertedAt.IsZero():
			a.emergency(ctx, send, "sys.agent_master_back", st, st.Since.Sub(alertedAt))
			alertedAt = time.Time{}
		}
	}
}

func (a *Agent) emergency(ctx context.Context, send func(context.Context, notify.Channel, *notify.Message) notify.Attempt, key string, st AgentStatus, d time.Duration) {
	lang := a.o.Lang
	body := i18n.T(lang, key, "node", st.Node, "master", strings.TrimPrefix(a.Server(), "https://"), "duration", i18n.Duration(d.Round(time.Second), lang))
	if st.LastError != "" && key == "sys.agent_master_down" {
		body += "\n" + st.LastError
	}
	msg := &notify.Message{Kind: "SYSTEM", Priority: "high", Title: i18n.T(lang, "sys.notice_title"), Body: body,
		Time: time.Now(), Instance: st.Node, Lang: lang, DeliveryID: fmt.Sprintf("%s-%s-%d", st.Node, key, time.Now().Unix())}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if at := send(sctx, *a.o.Emergency, msg); !at.OK {
		a.log.Error("emergency notification failed", "err", at.Error)
	} else {
		a.log.Warn("emergency notification sent", "message", body)
	}
}

// ParseEmergency reads "--emergency-webhook": a URL (generic webhook) or
// KIND:URL for a chat channel such as feishu:https://...
func ParseEmergency(s string) (*notify.Channel, error) {
	if s == "" {
		return nil, nil
	}
	kind, url := "webhook", s
	if k, rest, ok := strings.Cut(s, ":"); ok && !strings.HasPrefix(rest, "//") {
		kind, url = k, rest
	}
	ch := &notify.Channel{Name: "emergency", Kind: kind, Opts: map[string]string{"url": url}}
	if err := notify.ValidateChannel(kind, ch.Opts); err != nil {
		return nil, fmt.Errorf("--emergency-webhook: %w", err)
	}
	return ch, nil
}

// NormalizeServer turns a master address into https://host:port.
func NormalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid server URL %q", s)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("agents connect with TLS: use https://%s", u.Host)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "8077")
	}
	return "https://" + host, nil
}

func (a *Agent) addServer(u string) {
	for _, s := range a.servers {
		if s == u {
			return
		}
	}
	a.servers = append(a.servers, u)
}

// Server is the master address in use.
func (a *Agent) Server() string {
	a.smu.Lock()
	defer a.smu.Unlock()
	return a.servers[a.cur]
}

// Servers lists the known master addresses, the one in use first.
func (a *Agent) Servers() []string {
	a.smu.Lock()
	defer a.smu.Unlock()
	return append([]string{a.servers[a.cur]}, append(append([]string{}, a.servers[:a.cur]...), a.servers[a.cur+1:]...)...)
}

// next moves on to the following master address.
func (a *Agent) next() {
	a.smu.Lock()
	defer a.smu.Unlock()
	if len(a.servers) > 1 {
		a.cur = (a.cur + 1) % len(a.servers)
		a.log.Info("trying another master address", "server", a.servers[a.cur])
	}
}

// Term is the highest master term seen.
func (a *Agent) Term() uint64 {
	a.smu.Lock()
	defer a.smu.Unlock()
	return a.term
}

// setMasters takes the master's address list, keeping the one in use.
func (a *Agent) setMasters(list []string) {
	a.smu.Lock()
	cur := a.servers[a.cur]
	servers := []string{cur}
	for _, s := range list {
		if u, err := NormalizeServer(s); err == nil && u != cur {
			dup := false
			for _, x := range servers {
				dup = dup || x == u
			}
			if !dup {
				servers = append(servers, u)
			}
		}
	}
	changed := len(servers) != len(a.servers)
	for i := range servers {
		changed = changed || servers[i] != a.servers[(a.cur+i)%len(a.servers)]
	}
	a.servers, a.cur = servers, 0
	a.smu.Unlock()
	if changed && a.o.OnMasters != nil {
		a.o.OnMasters(servers)
	}
}

func hostPort(server string) (addr, host string) {
	u, _ := url.Parse(server)
	return u.Host, u.Hostname()
}

// FetchState downloads the master's state archive into w (standbys).
func (a *Agent) FetchState(ctx context.Context, w io.Writer) (key string, term uint64, err error) {
	addr, _ := hostPort(a.Server())
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return a.dialAddr(ctx, addr) },
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+StatePath, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.o.Token)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, fmt.Errorf("master answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	key = resp.Header.Get(StateKeyHeader)
	fmt.Sscan(resp.Header.Get(StateTermHeader), &term)
	if key == "" {
		return "", 0, errors.New("state without a key")
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return "", 0, err
	}
	return key, term, nil
}

// caps are the capabilities announced in the hello.
func (a *Agent) caps() []string {
	c := LocalCaps()
	if a.o.Replica {
		c = append(c, CapReplica)
	}
	return c
}

// follow adds the addresses a replaced master pointed to; the agent moves
// on to them (the term check still guards against an older master).
func (a *Agent) follow(servers []string) {
	if len(servers) == 0 {
		return
	}
	a.smu.Lock()
	cur := append([]string(nil), a.servers...)
	a.smu.Unlock()
	a.setMasters(append(cur, servers...))
}

// switchMaster puts server first and drops the current session so Run
// reconnects to it (used when a peer took over as master).
func (a *Agent) switchMaster(server string) {
	u, err := NormalizeServer(server)
	if err != nil {
		return
	}
	a.smu.Lock()
	rest := make([]string, 0, len(a.servers))
	for _, s := range a.servers {
		if s != u {
			rest = append(rest, s)
		}
	}
	a.servers, a.cur = append([]string{u}, rest...), 0
	a.smu.Unlock()
	if a.o.OnMasters != nil {
		a.o.OnMasters(a.Servers())
	}
	a.cancelSession()
}

func (a *Agent) cancelSession() {
	a.sessMu.Lock()
	c := a.sessCancel
	a.sessMu.Unlock()
	if c != nil {
		c()
	}
}

// PushStatus sends this node's status now instead of with the next ping
// answer, e.g. once it has its copy of the master's state.
func (a *Agent) PushStatus() {
	a.sessMu.Lock()
	s := a.live
	a.sessMu.Unlock()
	if s != nil {
		go s.send(msgStatus, a.status())
	}
}
