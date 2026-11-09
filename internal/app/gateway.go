package app

import (
	"sort"
	"sync"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/notify"
)

// gateways remembers how the last notification on each channel went out,
// so every node can show (and report to the others) whether its uplink to
// the notification gateways — SMS, webhooks, mail — works.
var gatewaysMu sync.Mutex

func (a *App) noteGateway(ch notify.Channel, at notify.Attempt) {
	if ch.Kind == "log" || ch.Kind == "group" {
		return
	}
	gatewaysMu.Lock()
	defer gatewaysMu.Unlock()
	if a.gateways == nil {
		a.gateways = map[string]engine.GatewayState{}
	}
	g := engine.GatewayState{Channel: ch.Name, Kind: ch.Kind, OK: at.OK, At: a.now()}
	if !at.OK {
		g.Error = at.Error
		if len(g.Error) > 200 {
			g.Error = g.Error[:200]
		}
	}
	a.gateways[ch.Name] = g
}

// GatewayStates lists the last outcome per channel, newest first.
func (a *App) GatewayStates() []engine.GatewayState {
	gatewaysMu.Lock()
	out := make([]engine.GatewayState, 0, len(a.gateways))
	for _, g := range a.gateways {
		out = append(out, g)
	}
	gatewaysMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// ReachView is this node's answer to a peer's check: its name and which
// nodes it reaches.
func (a *App) ReachView() (string, map[string]bool) {
	a.am.mu.Lock()
	ag, member := a.am.agent, a.am.member
	a.am.mu.Unlock()
	if member != nil {
		if ag == nil {
			return member.Node, map[string]bool{}
		}
		return member.Node, ag.Reach()
	}
	c := a.applied.Load()
	if c == nil {
		return "", map[string]bool{}
	}
	var names []string
	for _, n := range c.Nodes {
		if n.Name != c.MName {
			names = append(names, n.Name)
		}
	}
	return c.MName, a.master.Reach(names)
}

// Peers is a replica's view of the other nodes (nil on a master).
func (a *App) Peers() []cluster.PeerView {
	a.am.mu.Lock()
	ag := a.am.agent
	a.am.mu.Unlock()
	if ag == nil {
		return nil
	}
	return ag.Peers()
}

// feedPeers hands a replica's view of the other nodes to its own engine
// while it checks on its own, so it judges them by majority too.
func (a *App) feedPeers() {
	now := a.now()
	for _, v := range a.Peers() {
		st := engine.NodeStatus{Node: v.Name, Online: v.OK, Since: now, LastSeen: v.At, Reach: v.Reach, Listen: v.Addr}
		a.engine.NodeState(st)
	}
}
