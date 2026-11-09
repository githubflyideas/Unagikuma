package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

const peersFile = "cluster-peers.json"

// FullReachView is what this node answers on ReachPath: name, reach, term
// and role, so peers can detect a concurrent promote.
func (a *App) FullReachView() cluster.ReachView {
	node, reach := a.ReachView()
	v := cluster.ReachView{Node: node, Reach: reach}
	if a.isMember() {
		v.Role = "replica"
		a.am.mu.Lock()
		ag := a.am.agent
		a.am.mu.Unlock()
		if ag != nil {
			v.Term = ag.Term()
		}
		return v
	}
	if a.SupersededBy() != nil {
		return v
	}
	v.Role, v.Term = "master", a.Term()
	return v
}

// watchForeignMasters looks at peer Reach answers for another master. A
// higher term (or the same term with a lexicographically smaller name)
// wins: this server steps down. Equal-or-foreign masters also raise a
// CRITICAL "two masters" notice so the split is not silent. The lower
// term never auto-promotes; it only steps down to replica via supersede
// (and a later retire/join).
func (a *App) watchForeignMasters() {
	if a.isMember() || a.SupersededBy() != nil {
		return
	}
	self := a.nodeName()
	myTerm := a.Term()
	for _, addr := range a.peerListenAddrs() {
		rv, ok := a.probeReach(addr)
		if !ok || rv.Role != "master" || rv.Node == "" || rv.Node == self {
			continue
		}
		switch {
		case rv.Term > myTerm:
			a.log.Warn("another master has a higher term; stepping down", "peer", rv.Node, "term", rv.Term, "addr", addr)
			a.engine.TwoMasters(rv.Node, rv.Term)
			a.supersede(rv.Term, rv.Node)
			return
		case rv.Term == myTerm && rv.Node < self:
			a.log.Warn("another master has the same term and wins the name tie-break; stepping down", "peer", rv.Node, "term", rv.Term, "addr", addr)
			a.engine.TwoMasters(rv.Node, rv.Term)
			// Bump past the tie so agents prefer the winner once it is the only master.
			a.supersede(rv.Term+1, rv.Node)
			return
		case rv.Term == myTerm && rv.Node > self:
			a.engine.TwoMasters(rv.Node, rv.Term)
		}
	}
}

// peerListenAddrs lists places other nodes of this cluster may be listening:
// NODE standby= URLs, the previous-master list left by a promote, the
// membership master address list, live agent peer checks, the master's
// remembered agent listens, and cluster-peers.json persisted across restarts.
func (a *App) peerListenAddrs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return
		}
		if seen[u.Host] {
			return
		}
		seen[u.Host] = true
		out = append(out, u.Host)
	}
	if c := a.applied.Load(); c != nil {
		self := c.MName
		for _, n := range c.Nodes {
			if n.Name == self {
				continue
			}
			add(n.Standby)
		}
	}
	if b, err := os.ReadFile(a.dataPath(previousFile)); err == nil {
		var prev []string
		if json.Unmarshal(b, &prev) == nil {
			for _, s := range prev {
				add(s)
			}
		}
	}
	if b, err := os.ReadFile(a.dataPath(peersFile)); err == nil {
		var addrs []string
		if json.Unmarshal(b, &addrs) == nil {
			for _, s := range addrs {
				add(s)
			}
		}
	}
	a.am.mu.Lock()
	ag, member := a.am.agent, a.am.member
	a.am.mu.Unlock()
	if member != nil {
		add(member.Server)
		for _, s := range member.Servers {
			add(s)
		}
	}
	if ag != nil {
		for _, v := range ag.Peers() {
			add(v.Addr)
		}
	}
	if a.master != nil {
		for _, l := range a.master.PeerListens() {
			add(l)
		}
	}
	return out
}

func (a *App) probeReach(addr string) (cluster.ReachView, bool) {
	pins, expected := a.peerTrust(addr)
	tr := &http.Transport{Proxy: nil, TLSClientConfig: cluster.PinnedPeerTLSConfig(pins...)}
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: 3 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+cluster.ReachPath, nil)
	if err != nil {
		return cluster.ReachView{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return cluster.ReachView{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cluster.ReachView{}, false
	}
	var rv cluster.ReachView
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&rv) != nil {
		return cluster.ReachView{}, false
	}
	if expected != "" && rv.Node != expected {
		return cluster.ReachView{}, false
	}
	return rv, true
}

// persistPeerListens writes known peer listen addresses so a returning
// master (or a promote restart) can Reach them after a process restart.
func (a *App) persistPeerListens() {
	a.persistPeerIdentities()
	addrs := a.peerListenAddrs()
	if len(addrs) == 0 {
		return
	}
	b, _ := json.Marshal(addrs)
	_ = os.WriteFile(a.dataPath(peersFile), b, 0o644)
}

// competingMaster reports a reachable master whose term is >= wantTerm.
func (a *App) competingMaster(wantTerm uint64) (peer string, term uint64, ok bool) {
	self := ""
	a.am.mu.Lock()
	if a.am.member != nil {
		self = a.am.member.Node
	}
	a.am.mu.Unlock()
	if self == "" {
		self = a.nodeName()
	}
	for _, addr := range a.peerListenAddrs() {
		rv, reach := a.probeReach(addr)
		if !reach || rv.Role != "master" || rv.Node == "" || rv.Node == self {
			continue
		}
		if rv.Term >= wantTerm {
			return rv.Node, rv.Term, true
		}
	}
	return "", 0, false
}

// probeForeignMastersAtStart runs once before system notices fire for a
// returning old master: if a peer already has a newer term, step down
// immediately so this node does not briefly act as master (BUG-15 / Rule 2).
// No two_masters notice here — the node must not open system incidents or
// send master notifications before it knows it was replaced. It never
// auto-reclaims mastership; only a manual promote does.
func (a *App) probeForeignMastersAtStart() {
	if a.isMember() || a.SupersededBy() != nil {
		return
	}
	if len(a.peerListenAddrs()) == 0 {
		return
	}
	self := a.nodeName()
	myTerm := a.Term()
	for _, addr := range a.peerListenAddrs() {
		rv, ok := a.probeReach(addr)
		if !ok || rv.Role != "master" || rv.Node == "" || rv.Node == self {
			continue
		}
		if rv.Term > myTerm {
			a.log.Warn("another master has a higher term; stepping down at start", "peer", rv.Node, "term", rv.Term, "addr", addr)
			a.supersede(rv.Term, rv.Node)
			return
		}
	}
}
