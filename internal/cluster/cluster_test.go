package cluster

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

func TestPKI(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	p, err := LoadOrCreatePKI(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{caFile, caKeyFile, leafFile, leafKeyFile} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, err, st.Mode())
		}
	}
	again, err := LoadOrCreatePKI(dir, now)
	if err != nil || again.Fingerprint() != p.Fingerprint() {
		t.Fatalf("reload changed the CA: %v", err)
	}
	// A leaf close to expiry is reissued under the same CA.
	later, err := LoadOrCreatePKI(dir, now.Add(leafLifetime-leafRenewBefore/2))
	if err != nil || later.Fingerprint() != p.Fingerprint() || later.Leaf.Leaf.SerialNumber.Cmp(p.Leaf.Leaf.SerialNumber) == 0 {
		t.Fatalf("leaf not renewed: %v", err)
	}
	fp := p.Fingerprint()
	if !strings.HasPrefix(fp, "sha256:") || len(fp) != 7+64 {
		t.Fatal(fp)
	}
	colons := "SHA256:" + strings.ToUpper(fp[7:9]) + ":" + fp[9:]
	if n, err := normalizeFingerprint(colons); err != nil || n != fp {
		t.Fatalf("normalize: %q %v", n, err)
	}
	if _, err := normalizeFingerprint("sha256:abcd"); err == nil {
		t.Fatal("short fingerprint accepted")
	}
}

func TestSniffListener(t *testing.T) {
	p, _ := LoadOrCreatePKI(t.TempDir(), time.Now())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sl := SniffListener(ln, p.TLSConfig(nil), time.Second)
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			io.WriteString(w, "tls")
		} else {
			io.WriteString(w, "plain")
		}
	})}
	go hs.Serve(sl)
	defer hs.Close()

	// A client that never speaks must not block others.
	idle, _ := net.Dial("tcp", ln.Addr().String())
	defer idle.Close()

	get := func(c *http.Client, u string) string {
		t.Helper()
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if s := get(http.DefaultClient, "http://"+ln.Addr().String()+"/"); s != "plain" {
		t.Fatal(s)
	}
	pool := x509.NewCertPool()
	pool.AddCert(p.CA)
	tc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: AgentServerName}}}
	if s := get(tc, "https://"+ln.Addr().String()+"/"); s != "tls" {
		t.Fatal(s)
	}
}

// recSink records what the master reports.
type recSink struct {
	mu     sync.Mutex
	states []engine.NodeStatus
	votes  []engine.RemoteVote
}

func (s *recSink) NodeState(st engine.NodeStatus) {
	s.mu.Lock()
	s.states = append(s.states, st)
	s.mu.Unlock()
}

func (s *recSink) RemoteVote(v engine.RemoteVote) {
	s.mu.Lock()
	s.votes = append(s.votes, v)
	s.mu.Unlock()
}

func (s *recSink) last() (engine.NodeStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.states) == 0 {
		return engine.NodeStatus{}, false
	}
	return s.states[len(s.states)-1], true
}

func (s *recSink) vote(round string) (engine.RemoteVote, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.votes {
		if v.Round == round {
			return v, true
		}
	}
	return engine.RemoteVote{}, false
}

type clusterEnv struct {
	t       *testing.T
	revoked atomic.Bool
	pki     *PKI
	master  *Master
	sink    *recSink
	url     string
	hs      *http.Server
}

func newEnv(t *testing.T) *clusterEnv {
	t.Helper()
	p, err := LoadOrCreatePKI(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return newEnvWith(t, p, nil)
}

// newEnvWith runs a master with a given CA (masters of one cluster share
// it) and options adjusted by mod.
func newEnvWith(t *testing.T, p *PKI, mod func(*MasterOptions)) *clusterEnv {
	t.Helper()
	ts, _ := tunables.New(map[string]string{"cluster.ping-interval": "200ms"})
	sink := &recSink{}
	c := &clusterEnv{t: t, pki: p, sink: sink}
	mo := MasterOptions{
		Auth: func(token, addr string) (string, error) {
			switch {
			case token == "uka_bj" && !c.revoked.Load():
				return "bj", nil
			case token == "uka_gone":
				return "gone", nil
			}
			return "", errors.New("no such token")
		},
		Known:    func(n string) bool { return n == "bj" },
		Tunables: func() tunables.Set { return ts },
	}
	if mod != nil {
		mod(&mo)
	}
	m := NewMaster(mo)
	m.SetSink(sink)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET "+ConnectPath, m)
	mux.HandleFunc("GET "+StatePath, m.ServeState)
	hs := &http.Server{Handler: mux}
	go hs.Serve(SniffListener(ln, p.TLSConfig(nil), time.Second))
	t.Cleanup(func() { hs.Close(); m.Close() })
	c.master, c.url, c.hs = m, "https://"+ln.Addr().String(), hs
	return c
}

func (c *clusterEnv) agent(mod func(*AgentOptions)) (*Agent, func()) {
	c.t.Helper()
	o := AgentOptions{
		Server: c.url, Node: "bj", Token: "uka_bj", Fingerprint: c.pki.Fingerprint(), Version: "test", MaxBackoff: 200 * time.Millisecond,
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Latency: 2 * time.Millisecond, Detail: "probed " + s.Target}
		},
		Engine: func(d *engine.Deps) {
			d.Gateways = func() ([]netscope.Gateway, error) { return nil, nil }
			d.HostClock = func() (hostclock.Reading, bool) { return hostclock.Reading{}, false }
		},
	}
	if mod != nil {
		mod(&o)
	}
	a, err := NewAgent(o)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	stop := func() { cancel(); <-done }
	c.t.Cleanup(stop)
	return a, stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func agentConfig(serial uint32) map[string]*AgentConfig {
	return map[string]*AgentConfig{"bj": {
		Serial: serial, Hash: "cfg" + string(rune('0'+serial)),
		Monitors: []AgentMonitor{
			{Name: "web", DefHash: "h1", Probe: probe.Spec{Name: "web", Type: "TCP", Target: "10.0.0.5:80", Timeout: time.Second}},
			{Name: "db", DefHash: "h2", SecretError: "secret pw: not found", Probe: probe.Spec{Name: "db", Type: "TCP", Target: "10.0.0.6:5432", Timeout: time.Second}},
		},
		Tunables: map[string]string{"localnet.enabled": "off"},
	}}
}

func TestAgentSession(t *testing.T) {
	c := newEnv(t)
	c.master.SetConfigs(agentConfig(1))
	_, stop := c.agent(nil)

	waitFor(t, "agent online with config", func() bool {
		st, ok := c.sink.last()
		return ok && st.Online && st.Serial == 1 && st.Version == "test" && st.Health != nil
	})
	waitFor(t, "a ping round trip", func() bool {
		st, _ := c.sink.last()
		return st.RTT > 0 && absDur(st.Skew) < time.Second
	})

	if !c.master.Probe("bj", engine.ProbeRequest{Round: "r1", Monitor: "web", DefHash: "h1"}) {
		t.Fatal("probe not sent")
	}
	c.master.Probe("bj", engine.ProbeRequest{Round: "r2", Monitor: "web", DefHash: "stale"})
	c.master.Probe("bj", engine.ProbeRequest{Round: "r3", Monitor: "db", DefHash: "h2"})
	c.master.Probe("bj", engine.ProbeRequest{Round: "r4", Monitor: "nope", DefHash: "x"})
	waitFor(t, "votes", func() bool {
		_, a := c.sink.vote("r1")
		_, b := c.sink.vote("r4")
		return a && b
	})
	v, _ := c.sink.vote("r1")
	if v.Node != "bj" || v.Vote != probe.VoteOK || v.DefHash != "h1" || v.Result.Detail != "probed 10.0.0.5:80" {
		t.Fatalf("r1: %+v", v)
	}
	waitFor(t, "r2 r3", func() bool {
		_, a := c.sink.vote("r2")
		_, b := c.sink.vote("r3")
		return a && b
	})
	if v, _ := c.sink.vote("r2"); v.DefHash != "h1" {
		t.Fatalf("stale definition must answer with the agent's own hash: %+v", v)
	}
	if v, _ := c.sink.vote("r3"); v.Vote != probe.VoteAbstain || v.Reason != probe.ReasonSecretUnavailable {
		t.Fatalf("r3: %+v", v)
	}
	if v, _ := c.sink.vote("r4"); v.Vote != probe.VoteAbstain || v.Reason != probe.ReasonConfig {
		t.Fatalf("r4: %+v", v)
	}

	// A new configuration reaches the connected agent.
	c.master.SetConfigs(agentConfig(2))
	waitFor(t, "serial 2", func() bool { st, _ := c.sink.last(); return st.Serial == 2 })

	stop()
	waitFor(t, "offline after disconnect", func() bool { st, _ := c.sink.last(); return !st.Online })
	if c.master.Probe("bj", engine.ProbeRequest{Round: "r5", Monitor: "web", DefHash: "h1"}) {
		t.Fatal("probe to a disconnected node reported sent")
	}
}

func TestAgentReconnectsAndReplaces(t *testing.T) {
	c := newEnv(t)
	c.master.SetConfigs(agentConfig(1))
	c.agent(nil)
	waitFor(t, "first agent", func() bool { st, ok := c.sink.last(); return ok && st.Online })
	// A second agent with the same token takes over the session; the
	// first one reconnects in turn. The node never shows offline.
	c.agent(nil)
	time.Sleep(time.Second)
	c.sink.mu.Lock()
	for _, st := range c.sink.states {
		if !st.Online {
			c.sink.mu.Unlock()
			t.Fatalf("node reported offline while replaced: %+v", st)
		}
	}
	c.sink.mu.Unlock()
	if len(c.master.Connected()) != 1 {
		t.Fatalf("connected: %v", c.master.Connected())
	}
}

func TestAgentRefused(t *testing.T) {
	c := newEnv(t)
	try := func(mod func(*AgentOptions)) error {
		t.Helper()
		o := AgentOptions{Server: c.url, Token: "uka_bj", Fingerprint: c.pki.Fingerprint()}
		mod(&o)
		a, err := NewAgent(o)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return a.session(ctx)
	}
	var ae *AuthError
	if err := try(func(o *AgentOptions) { o.Token = "uka_wrong" }); !errors.As(err, &ae) {
		t.Fatalf("bad token: %v", err)
	}
	if err := try(func(o *AgentOptions) { o.Node = "sh" }); !errors.As(err, &ae) || !strings.Contains(err.Error(), "belongs to node bj") {
		t.Fatalf("token of another node: %v", err)
	}
	if err := try(func(o *AgentOptions) { o.Token = "uka_gone" }); !errors.As(err, &ae) || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("undeclared node: %v", err)
	}
	other, _ := LoadOrCreatePKI(t.TempDir(), time.Now())
	if err := try(func(o *AgentOptions) { o.Fingerprint = other.Fingerprint() }); err == nil || !strings.Contains(err.Error(), "pinned fingerprint") {
		t.Fatalf("wrong pin: %v", err)
	}
	if _, err := NewAgent(AgentOptions{Server: c.url, Token: "x"}); err == nil {
		t.Fatal("missing fingerprint accepted")
	}
	if _, err := NewAgent(AgentOptions{Server: "http://h:1", Token: "x", Insecure: true}); err == nil {
		t.Fatal("plain http accepted")
	}

	// The upgrade endpoint refuses plain HTTP: sessions carry secrets.
	resp, err := http.Get(strings.Replace(c.url, "https", "http", 1) + ConnectPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain upgrade: %d", resp.StatusCode)
	}
}

func TestProtocolCompat(t *testing.T) {
	for v, want := range map[string]bool{"1.0": true, "1.1": true, "2.0": false, "0.9": false, "x": false, "1": false} {
		if compatible(v) != want {
			t.Errorf("compatible(%q) = %v", v, !want)
		}
	}
	r, w := io.Pipe()
	go func() { writeFrame(w, msgPing, Ping{Seq: 7}); w.Close() }()
	if _, err := readFrame(r, 8); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func TestRevokedTokenEndsSession(t *testing.T) {
	c := newEnv(t)
	c.master.SetConfigs(agentConfig(1))
	c.agent(nil)
	waitFor(t, "online", func() bool { st, ok := c.sink.last(); return ok && st.Online })
	// Revoked elsewhere (another process sharing the database): the
	// session ends at the next ping and the agent cannot come back.
	c.revoked.Store(true)
	waitFor(t, "offline after revocation", func() bool { st, _ := c.sink.last(); return !st.Online })
	time.Sleep(500 * time.Millisecond)
	if st, _ := c.sink.last(); st.Online || len(c.master.Connected()) != 0 {
		t.Fatal("revoked agent reconnected")
	}
}

func TestConcurrencyChangeDuringProbe(t *testing.T) {
	c := newEnv(t)
	c.master.SetConfigs(agentConfig(1))
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	c.agent(func(o *AgentOptions) {
		o.Probe = func(context.Context, probe.Spec) probe.Result {
			select {
			case started <- struct{}{}:
				<-release
			default:
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK}
		}
	})
	waitFor(t, "online", func() bool { st, ok := c.sink.last(); return ok && st.Serial == 1 })
	c.master.Probe("bj", engine.ProbeRequest{Round: "slow", Monitor: "web", DefHash: "h1"})
	<-started
	// A new configuration resizes the probe limit while that probe runs.
	cfg := agentConfig(2)
	cfg["bj"].Tunables["probe.max-concurrency"] = "1"
	c.master.SetConfigs(cfg)
	waitFor(t, "serial 2", func() bool { st, _ := c.sink.last(); return st.Serial == 2 })
	close(release)
	waitFor(t, "the slow probe's vote", func() bool { _, ok := c.sink.vote("slow"); return ok })
	c.master.Probe("bj", engine.ProbeRequest{Round: "next", Monitor: "web", DefHash: "h1"})
	waitFor(t, "a later vote", func() bool { _, ok := c.sink.vote("next"); return ok })
}

// rawHello upgrades by hand and sends a hello with the given protocol
// version, returning the master's first answer.
func rawHello(t *testing.T, c *clusterEnv, proto string) frame {
	t.Helper()
	conn, err := tls.Dial("tcp", strings.TrimPrefix(c.url, "https://"), &tls.Config{
		ServerName: AgentServerName, NextProtos: []string{"http/1.1"}, InsecureSkipVerify: true,
		VerifyConnection: pinnedVerifier(c.pki.Fingerprint()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req, _ := http.NewRequest("GET", c.url+ConnectPath, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", Upgrade)
	req.Header.Set("Authorization", "Bearer uka_bj")
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", err, resp)
	}
	if err := writeFrame(conn, msgHello, Hello{Proto: proto, Node: "bj", Version: "other", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	f, err := readFrame(br, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNeighbourProtocolVersions(t *testing.T) {
	c := newEnv(t)
	next := fmt.Sprintf("%d.%d", ProtoMajor, ProtoMinor+1)
	if f := rawHello(t, c, next); f.Type != msgWelcome {
		t.Fatalf("an agent one minor version ahead must be served: %+v", f)
	}
	f := rawHello(t, c, fmt.Sprintf("%d.0", ProtoMajor+1))
	var e ErrorMsg
	json.Unmarshal(f.Data, &e)
	if f.Type != msgError || e.Code != "version" || !strings.Contains(e.Message, "upgrade") {
		t.Fatalf("an agent of another major version must be told to upgrade: %+v %+v", f, e)
	}
}

func TestEmergencyWhenMasterIsAway(t *testing.T) {
	c := newEnv(t)
	c.master.SetConfigs(agentConfig(1))
	var mu sync.Mutex
	var sent []string
	ch, err := ParseEmergency("feishu:https://open.feishu.cn/open-apis/bot/v2/hook/x")
	if err != nil || ch.Kind != "feishu" {
		t.Fatalf("parse: %v %+v", err, ch)
	}
	c.agent(func(o *AgentOptions) {
		o.Emergency, o.EmergencyAfter = ch, 500*time.Millisecond
		o.Send = func(_ context.Context, _ notify.Channel, m *notify.Message) notify.Attempt {
			mu.Lock()
			sent = append(sent, m.Body)
			mu.Unlock()
			return notify.Attempt{OK: true}
		}
	})
	waitFor(t, "online", func() bool { st, ok := c.sink.last(); return ok && st.Online })
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(sent) }
	// The master goes away: one message after the delay, not more.
	c.revoked.Store(true)
	waitFor(t, "emergency message", func() bool { return count() == 1 })
	time.Sleep(time.Second)
	if count() != 1 {
		t.Fatalf("one message per outage: %v", sent)
	}
	c.revoked.Store(false)
	waitFor(t, "recovery message", func() bool { return count() == 2 })
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(sent[0], "cannot reach master") || !strings.Contains(sent[1], "again") {
		t.Fatalf("messages: %q", sent)
	}
	if _, err := ParseEmergency("https://hooks.example.com/x"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEmergency("nosuchkind:https://x"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
