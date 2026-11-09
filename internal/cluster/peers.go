package cluster

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// Every site checks known peers for informational reachability and authenticated
// configuration-authority discovery. Reach never gates target alerts.

// PeerView is what this node knows about one peer.
type PeerView struct {
	Name        string          `json:"name"`
	Addr        string          `json:"addr"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	OK          bool            `json:"ok"`
	At          time.Time       `json:"at"`
	Error       string          `json:"error,omitempty"`
	Reach       map[string]bool `json:"reach,omitempty"` // the peer's own view
	Term        uint64          `json:"term,omitempty"`
	Role        string          `json:"role,omitempty"`
}

type peerSet struct {
	mu          sync.Mutex
	master      string
	masterPin   string
	views       map[string]*PeerView
	masterReach map[string]bool // the master's own view, as last fetched
}

func (a *Agent) setPeers(p Peers) {
	ps := &a.peers
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.views == nil {
		ps.views = map[string]*PeerView{}
	}
	ps.master = p.Master
	ps.masterPin = p.MasterFingerprint
	keep := map[string]bool{}
	for _, n := range p.Nodes {
		if n.Name == a.o.Node {
			continue
		}
		keep[n.Name] = true
		if v := ps.views[n.Name]; v != nil {
			v.Addr, v.Fingerprint = n.Addr, n.Fingerprint
		} else {
			ps.views[n.Name] = &PeerView{Name: n.Name, Addr: n.Addr, Fingerprint: n.Fingerprint}
		}
	}
	for n := range ps.views {
		if !keep[n] {
			delete(ps.views, n)
		}
	}
}

// Peers returns this node's view of the other nodes, the master first.
// The master's entry reflects the agent connection.
func (a *Agent) Peers() []PeerView {
	st := a.Status()
	ps := &a.peers
	ps.mu.Lock()
	out := make([]PeerView, 0, len(ps.views)+1)
	master := ps.master
	masterPin := ps.masterPin
	for _, v := range ps.views {
		c := *v
		out = append(out, c)
	}
	ps.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if master != "" {
		mv := PeerView{Fingerprint: masterPin, Name: master, Addr: hostOf(a.Server()), OK: st.Connected, At: time.Now()}
		if mr, ok := a.masterView(); ok {
			mv.Reach = mr
		}
		out = append([]PeerView{mv}, out...)
	}
	return out
}

// masterView is the master's own reach, as last fetched.
func (a *Agent) masterView() (map[string]bool, bool) {
	a.peers.mu.Lock()
	defer a.peers.mu.Unlock()
	return a.peers.masterReach, a.peers.masterReach != nil
}

// Reach is which nodes this one reaches: the master (connected) and every
// peer that answered its last check.
func (a *Agent) Reach() map[string]bool {
	out := map[string]bool{}
	for _, v := range a.Peers() {
		out[v.Name] = v.OK
	}
	return out
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return u
}

// checkPeers runs a check of every peer each interval while ctx lives.
func (a *Agent) checkPeers(ctx context.Context) {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		Proxy:           nil,
		TLSClientConfig: PinnedTLSConfig(a.pin),
	}}
	for {
		a.mu.Lock()
		every := a.ts.Duration("cluster.peer-interval")
		a.mu.Unlock()
		a.peers.mu.Lock()
		todo := make([]PeerView, 0, len(a.peers.views))
		for _, v := range a.peers.views {
			todo = append(todo, *v)
		}
		a.peers.mu.Unlock()
		var wg sync.WaitGroup
		for _, v := range todo {
			wg.Add(1)
			go func(v PeerView) {
				defer wg.Done()
				peerClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: PinnedPeerTLSConfig(v.Fingerprint)}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				res := a.checkPeer(ctx, peerClient, v.Addr)
				if res.OK && res.Name != v.Name {
					res = PeerView{Addr: v.Addr, At: time.Now(), Error: "peer identity mismatch"}
				}
				if tr, ok := peerClient.Transport.(*http.Transport); ok {
					tr.CloseIdleConnections()
				}
				a.peers.mu.Lock()
				if cur := a.peers.views[v.Name]; cur != nil && cur.Addr == v.Addr {
					cur.OK, cur.At, cur.Error, cur.Reach = res.OK, res.At, res.Error, res.Reach
					cur.Term, cur.Role = res.Term, res.Role
					if res.Name != "" {
						cur.Name = res.Name
					}
				}
				a.peers.mu.Unlock()
			}(v)
		}
		// The master's own view, when it is reachable on the address in use.
		if a.Status().Connected {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res := a.checkPeer(ctx, client, hostOf(a.Server()))
				if res.OK && res.Reach != nil {
					a.peers.mu.Lock()
					a.peers.masterReach = res.Reach
					a.peers.mu.Unlock()
				}
			}()
		}
		wg.Wait()
		a.followPeerMaster()
		select {
		case <-ctx.Done():
			return
		case <-a.peersKick:
			// Reconnected to the master: re-check peers right away (BUG-16).
		case <-time.After(every):
		}
	}
}

func (a *Agent) checkPeer(ctx context.Context, c *http.Client, addr string) PeerView {
	v := PeerView{Addr: addr, At: time.Now()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+ReachPath, nil)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	resp, err := c.Do(req)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	defer resp.Body.Close()
	var rv ReachView
	switch {
	case resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&rv) == nil:
		v.OK, v.Reach, v.Name, v.Term, v.Role = true, rv.Reach, rv.Node, rv.Term, rv.Role
		return v
	case resp.StatusCode == http.StatusNotFound:
		// Older releases have no ReachPath: reachable, no view (BUG-13).
		v.OK = true
		return v
	}
	// Any other answer still proves reachability.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	v.OK = true
	return v
}

// ServeReach answers a peer's check with this node's name, view and role.
func ServeReach(w http.ResponseWriter, v ReachView) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// followPeerMaster switches to a peer that answered Reach as a master with
// a newer term (or the same term and a winning name). Used when the old
// master is down and another replica was promoted (BUG-10, BUG-11).
func (a *Agent) followPeerMaster() {
	known := a.Term()
	self := a.o.Node
	a.peers.mu.Lock()
	var cand *PeerView
	for _, v := range a.peers.views {
		if !v.OK || v.Role != "master" || v.Name == "" || v.Name == self {
			continue
		}
		better := v.Term > known || (v.Term == known && known > 0 && v.Name < self)
		if !better {
			continue
		}
		if cand == nil || v.Term > cand.Term || (v.Term == cand.Term && v.Name < cand.Name) {
			c := *v
			cand = &c
		}
	}
	a.peers.mu.Unlock()
	if cand == nil || cand.Addr == "" {
		return
	}
	u := "https://" + cand.Addr
	if hostOf(a.Server()) == cand.Addr {
		return // already on that master
	}
	a.log.Warn("following a peer that took over as master", "peer", cand.Name, "term", cand.Term, "server", u)
	if cand.Term > known {
		a.smu.Lock()
		a.term = cand.Term
		a.smu.Unlock()
		if a.o.OnTerm != nil {
			a.o.OnTerm(cand.Term)
		}
	}
	a.switchMaster(u)
}
