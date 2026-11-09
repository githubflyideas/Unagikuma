package app

import (
	"context"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

// followMasterAsReplica asks the winning master at addr for a join code and
// demotes into a replica. Never auto-promotes; preserves local site-pauses
// and ACKs because demote only joins and reapplies config overlays.
func (a *App) followMasterAsReplica(addr, peerName string) {
	if a.isMember() {
		return
	}
	if a.SupersededBy() == nil {
		return
	}
	if a.pki == nil || a.pki.Fingerprint() == "" {
		return
	}
	want := a.nodeName()
	if s := a.SupersededBy(); s != nil && s.By != "" && peerName == "" {
		peerName = s.By
	}
	_ = peerName
	req, err := a.pki.SignRejoin(want, a.now())
	if err != nil {
		a.log.Warn("cannot sign rejoin request", "err", err)
		return
	}
	server := addr
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := cluster.RequestRejoinOffer(ctx, server, a.pki.Fingerprint(), req)
	if err != nil {
		a.log.Warn("could not obtain rejoin offer from new master", "addr", addr, "err", err)
		return
	}
	if n.Node == "" {
		n.Node = want
	}
	a.log.Info("obtained rejoin offer; demoting to replica", "addr", addr, "node", n.Node)
	a.demote(n)
}

// tryFollowAfterSupersede kicks a background attempt to rejoin the winner.
func (a *App) tryFollowAfterSupersede(addr, peerName string) {
	go func() {
		// Brief pause so the winner finishes promote bookkeeping.
		time.Sleep(2 * time.Second)
		a.followMasterAsReplica(addr, peerName)
	}()
}

// watchSupersededRejoin retries joining while this node is stuck replaced.
func (a *App) watchSupersededRejoin() {
	if a.isMember() || a.SupersededBy() == nil {
		return
	}
	addrs := a.peerListenAddrs()
	if s := a.SupersededBy(); s != nil && s.By != "" {
		// Prefer probing named peers first when we only have listens.
		_ = s
	}
	for _, addr := range addrs {
		rv, ok := a.probeReach(addr)
		if !ok || rv.Role != "master" {
			continue
		}
		a.followMasterAsReplica(addr, rv.Node)
		if a.isMember() {
			return
		}
	}
}
