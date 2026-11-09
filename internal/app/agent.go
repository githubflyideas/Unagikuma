package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
)

// A server that joined a cluster runs as one of its agents. Membership is
// kept in the data directory so it survives restarts.
const (
	agentFile      = "agent.json"
	agentTokenFile = "agent.token"
)

// Membership is this server's place in a cluster it joined.
type Membership struct {
	Server string `json:"server"`
	// Servers are further master addresses (standbys), Term the highest
	// master term served: both survive restarts so a replaced master is
	// not followed again.
	Servers     []string  `json:"servers,omitempty"`
	Term        uint64    `json:"term,omitempty"`
	Node        string    `json:"node"`
	Fingerprint string    `json:"fingerprint"`
	Joined      time.Time `json:"joined"`
}

type agentMode struct {
	mu     sync.Mutex
	member *Membership
	agent  *cluster.Agent
	cancel context.CancelFunc
	done   chan struct{}
	runCtx context.Context
}

// ErrAlreadyJoined means this server already is a node of a cluster.
var ErrAlreadyJoined = errors.New("this server already belongs to a cluster; leave it first")

func (a *App) loadMembership() {
	b, err := os.ReadFile(filepath.Join(a.opts.DataDir, agentFile))
	if err != nil {
		return
	}
	var m Membership
	if json.Unmarshal(b, &m) == nil && m.Server != "" {
		a.am.member = &m
	}
}

// startAgent runs the agent while ctx lives (lock held).
func (a *App) startAgent() error {
	am := &a.am
	if am.member == nil || am.runCtx == nil || am.agent != nil {
		return nil
	}
	tok, err := os.ReadFile(filepath.Join(a.opts.DataDir, agentTokenFile))
	if err != nil {
		return fmt.Errorf("agent token: %w", err)
	}
	var initialPeers []cluster.PeerAddr
	if b, err := os.ReadFile(a.dataPath(peerIdentityFile)); err == nil {
		_ = json.Unmarshal(b, &initialPeers)
	}
	ag, err := cluster.NewAgent(cluster.AgentOptions{
		Server: am.member.Server, Node: am.member.Node, Token: strings.TrimSpace(string(tok)),
		Fingerprint: am.member.Fingerprint, Version: a.opts.Version, Log: a.log.With("component", "agent"),
		Probe: a.opts.Probe, Servers: am.member.Servers, Term: am.member.Term, Replica: true,
		InitialPeers: initialPeers, Listen: a.opts.Listen, PeerFingerprint: a.pki.PeerIdentity().Fingerprint(), Gateway: a.GatewayStates,
		OnTerm:    func(t uint64) { a.updateMembership(func(m *Membership) { m.Term = t }) },
		OnMasters: func(l []string) { a.updateMembership(func(m *Membership) { m.Server, m.Servers = l[0], l[1:] }) },
		OnState:   a.onStateNotice,
		Synced:    a.replicaSynced,
		OnPromote: func(t uint64) {
			if _, err := a.Promote(context.Background(), t, "handover"); err != nil {
				a.log.Error("handover from the master failed", "err", err)
			}
		},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(am.runCtx)
	done := make(chan struct{})
	am.agent, am.cancel, am.done = ag, cancel, done
	go func() { defer close(done); _ = ag.Run(ctx) }()
	a.log.Info("running as a node of a cluster", "master", am.member.Server, "node", am.member.Node)
	return nil
}

func (a *App) runAgentMode(ctx context.Context) {
	a.am.mu.Lock()
	a.am.runCtx = ctx
	if err := a.startAgent(); err != nil {
		a.log.Error("cannot start the agent", "err", err)
	}
	a.am.mu.Unlock()
	<-ctx.Done()
	a.am.mu.Lock()
	done := a.am.done
	a.am.mu.Unlock()
	if done != nil {
		<-done
	}
}

// AgentState is this server's cluster membership and connection.
type AgentState struct {
	Member *Membership          `json:"member"`
	Status *cluster.AgentStatus `json:"status,omitempty"`
}

// Agent reports the membership (nil member: not joined).
func (a *App) Agent() AgentState {
	a.am.mu.Lock()
	defer a.am.mu.Unlock()
	st := AgentState{Member: a.am.member}
	if a.am.agent != nil {
		s := a.am.agent.Status()
		st.Status = &s
	}
	return st
}

// JoinOptions describe a join from this server's UI.
type JoinOptions struct {
	Code   string
	Node   string
	Title  string
	Region string
	// Import offers this server's monitors to the master (added paused
	// under a section named after the node). Either way they are removed
	// here: a node only runs what the master gives it.
	Import bool
	Author string
}

// JoinCluster turns this server into a node of the cluster in the code.
func (a *App) JoinCluster(ctx context.Context, o JoinOptions) (cluster.JoinResponse, error) {
	var resp cluster.JoinResponse
	a.am.mu.Lock()
	defer a.am.mu.Unlock()
	if a.am.member != nil {
		return resp, ErrAlreadyJoined
	}
	info, err := cluster.DecodeJoin(o.Code)
	if err != nil {
		return resp, err
	}
	if !info.Expires.IsZero() && a.now().After(info.Expires) {
		return resp, errors.New("the join code has expired; generate a new one on the master")
	}
	cur := a.Config()
	req := cluster.JoinRequest{Code: info.Code, Node: o.Node, Title: o.Title, Region: o.Region,
		Version: a.opts.Version, Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if o.Import {
		for _, m := range cur.Config.Monitors {
			r, ok := cur.File.Record(m.Name)
			if !ok {
				continue
			}
			ir := cluster.ImportRecord{Name: m.Name, Type: r.Type, Target: r.Target, Interval: r.Interval, Opts: map[string]string{}}
			for _, op := range r.Opts {
				ir.Opts[op.Key] = op.Value
			}
			req.Import = append(req.Import, ir)
		}
	}
	resp, err = cluster.Join(ctx, info, req)
	if err != nil {
		return resp, err
	}
	m := &Membership{Server: info.Server, Node: resp.Node, Fingerprint: info.Fingerprint, Joined: a.now()}
	if err := a.writeMembership(m, resp.Token); err != nil {
		return resp, err
	}
	a.am.member = m
	// The master decides what this node checks from now on. Drop local
	// monitors, maints and NODE rows left from a previous master role so a
	// demoted master does not keep judging stale peers (BUG-9).
	if _, err := a.cfg.edit(o.Author, "joined cluster "+info.Server+" as "+resp.Node, nil, func(f *config.File) error {
		for _, mon := range cur.Config.Monitors {
			if err := f.DeleteRecord(mon.Name); err != nil {
				return err
			}
		}
		for _, mt := range cur.Config.Maints {
			if err := f.DeleteRecord(mt.Name); err != nil {
				return err
			}
		}
		for _, n := range cur.Config.Nodes {
			if err := f.DeleteRecord(n.Name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		a.log.Warn("could not clear local monitors after joining", "err", err)
	}
	if err := a.startAgent(); err != nil {
		return resp, err
	}
	return resp, nil
}

func (a *App) writeMembership(m *Membership, token string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	dir := a.opts.DataDir
	if err := os.WriteFile(filepath.Join(dir, agentTokenFile), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, agentFile), b, 0o600)
}

// LeaveCluster stops the agent and forgets the membership. The master
// still lists the node until it is removed there.
func (a *App) LeaveCluster() error {
	a.am.mu.Lock()
	defer a.am.mu.Unlock()
	if a.am.member == nil {
		return nil
	}
	if a.am.cancel != nil {
		a.am.cancel()
		<-a.am.done
	}
	a.am.agent, a.am.cancel, a.am.done, a.am.member = nil, nil, nil, nil
	dir := a.opts.DataDir
	_ = os.Remove(filepath.Join(dir, agentTokenFile))
	return os.Remove(filepath.Join(dir, agentFile))
}

// CreateJoinCode issues a join code naming this server at server.
func (a *App) CreateJoinCode(server string) (string, time.Time, error) {
	ttl := a.Config().Config.Tunables.Duration("cluster.join-code-ttl")
	code, exp, err := a.store.CreateJoinCode(ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	return cluster.EncodeJoin(cluster.JoinInfo{Server: server, Fingerprint: a.pki.Fingerprint(), Code: code, Expires: exp}), exp, nil
}

// ErrNodeName means a joining machine asked for an unusable node name.
var ErrNodeName = errors.New("invalid node name")

// AcceptJoin adds a machine that presented a valid join code as a node:
// its NODE record, its imported monitors (paused, in a section named after
// it) and its token.
func (a *App) AcceptJoin(req cluster.JoinRequest) (cluster.JoinResponse, error) {
	resp := cluster.JoinResponse{Node: req.Node}
	cfg := a.Config().Config
	// Only the immediately replaced master may reclaim its existing NODE
	// record, and it must still present a valid one-use join capability.
	rejoin := false
	if p, ok := a.loadPromoted(); ok && p.Self == cfg.MName && p.OldMaster == req.Node {
		for _, n := range cfg.Nodes {
			if n.Name == req.Node {
				rejoin = true
			}
		}
	}
	switch {
	case !config.ValidName(req.Node):
		return resp, fmt.Errorf("%w %q", ErrNodeName, req.Node)
	case req.Node == cfg.MName || (cfg.HasName(req.Node) && !rejoin):
		return resp, fmt.Errorf("%w: %q is already used in monitors.conf", ErrNodeName, req.Node)
	}
	if !rejoin {
		if err := a.AllowAdd("nodes"); err != nil {
			return resp, err
		}
	}
	if err := a.store.UseJoinCode(req.Code, req.Node); err != nil {
		return resp, err
	}
	node := config.RecordSpec{Name: req.Node, Type: "NODE", Target: req.Title}
	if req.Region != "" {
		node.Opts = append(node.Opts, config.Opt{Key: "region", Value: req.Region})
	}
	author := "join:" + req.Node
	imports, names := importSpecs(cfg, req)
	if rejoin {
		imports, names = nil, nil
	}
	_, err := a.EditConfig(author, "node "+req.Node+" joined", func(f *config.File) error {
		if !rejoin {
			if err := f.UpsertRecord(node); err != nil {
				return err
			}
		}
		for _, sp := range imports {
			if err := f.UpsertRecord(sp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && len(imports) > 0 {
		// Keep the node; drop the monitors it brought.
		resp.Skipped = err.Error()
		names = nil
		_, err = a.EditConfig(author, "node "+req.Node+" joined", func(f *config.File) error { return f.UpsertRecord(node) })
	}
	if err != nil {
		a.store.ReleaseJoinCode(req.Code)
		return resp, err
	}
	resp.Imported = names
	if resp.Token, err = a.store.CreateAgentToken(req.Node); err != nil {
		return resp, err
	}
	a.log.Info("node joined", "node", req.Node, "version", req.Version, "platform", req.Platform, "imported", len(names))
	return resp, nil
}

// importSpecs turns a joining machine's monitors into paused records in a
// section of their own. References to its secrets, policies and other
// monitors mean nothing here and are dropped; names that clash get the node
// name as a prefix.
func importSpecs(cfg *config.Config, req cluster.JoinRequest) ([]config.RecordSpec, []string) {
	drop := map[string]bool{"policy": true, "parent": true, "nodes": true, "disabled": true}
	var out []config.RecordSpec
	var names []string
	taken := map[string]bool{req.Node: true}
	for _, ir := range req.Import {
		name := ir.Name
		if cfg.HasName(name) || taken[name] {
			name = req.Node + "-" + name
		}
		if !config.ValidName(name) || cfg.HasName(name) || taken[name] || !config.IsMonitorType(strings.ToUpper(ir.Type)) || strings.Contains(ir.Target, "${") {
			continue
		}
		taken[name] = true
		sp := config.RecordSpec{Name: name, Type: ir.Type, Target: ir.Target, Interval: ir.Interval, Area: req.Node,
			Comment: "imported from " + req.Node + "; review and resume"}
		keys := make([]string, 0, len(ir.Opts))
		for k := range ir.Opts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := ir.Opts[k]
			if drop[k] || strings.Contains(v, "${") {
				continue
			}
			sp.Opts = append(sp.Opts, config.Opt{Key: k, Value: v})
		}
		sp.Opts = append(sp.Opts, config.Opt{Key: "disabled", Value: "yes"})
		out = append(out, sp)
		names = append(names, name)
	}
	return out, names
}

// updateMembership changes and saves the membership.
func (a *App) updateMembership(f func(*Membership)) {
	a.am.mu.Lock()
	defer a.am.mu.Unlock()
	if a.am.member == nil {
		return
	}
	m := *a.am.member
	f(&m)
	a.am.member = &m
	b, _ := json.MarshalIndent(&m, "", "  ")
	if err := os.WriteFile(filepath.Join(a.opts.DataDir, agentFile), b, 0o600); err != nil {
		a.log.Warn("save membership", "err", err)
	}
}

// onStateNotice fetches the master's state when told to (standbys).
func (a *App) onStateNotice() {
	a.am.mu.Lock()
	ag, ctx := a.am.agent, a.am.runCtx
	a.am.mu.Unlock()
	if ag == nil || ctx == nil {
		return
	}
	if !a.stateBusy.CompareAndSwap(false, true) {
		return
	}
	defer a.stateBusy.Store(false)
	if _, err := a.fetchState(ctx, ag); err != nil {
		a.log.Warn("cannot copy the master's state", "err", err)
		// Try again soon rather than at the next notice, which may be a
		// whole sync interval away (a refused or interrupted copy).
		if a.stateRetries.Add(1) <= 3 {
			time.AfterFunc(a.Config().Config.Tunables.Duration("cluster.standby-retry"), a.onStateNotice)
		}
		return
	}
	a.stateRetries.Store(0)
	ag.PushStatus() // the master counts a new member online once it has the state
}
