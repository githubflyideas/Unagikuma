package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

// followMasterAsReplica asks the winning master at addr for a join code and
// demotes into a replica. Never auto-promotes; preserves local site-pauses
// and ACKs because demote only joins and reapplies config overlays.
func (a *App) followMasterAsReplica(addr string) {
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
func (a *App) tryFollowAfterSupersede(addr string) {
	go func() {
		// Brief pause so the winner finishes promote bookkeeping.
		time.Sleep(2 * time.Second)
		a.followMasterAsReplica(addr)
	}()
}

// pendingRejoinFile keeps the new master's offer whose join failed.
const pendingRejoinFile = "cluster-rejoin-pending.json"

func (a *App) savePendingRejoin(n cluster.RetireNotice) {
	b, err := json.Marshal(n)
	if err == nil {
		_ = os.WriteFile(a.dataPath(pendingRejoinFile), b, 0o600)
	}
}

// retryPendingRejoin joins with a kept offer while its code is valid.
func (a *App) retryPendingRejoin() {
	b, err := os.ReadFile(a.dataPath(pendingRejoinFile))
	if err != nil {
		return
	}
	var n cluster.RetireNotice
	if json.Unmarshal(b, &n) != nil {
		_ = os.Remove(a.dataPath(pendingRejoinFile))
		return
	}
	if info, err := cluster.DecodeJoin(n.Join); err != nil || !info.Expires.IsZero() && a.now().After(info.Expires) {
		a.log.Warn("the new master's rejoin offer expired; asking it for a new one")
		_ = os.Remove(a.dataPath(pendingRejoinFile))
		return
	}
	a.demote(n)
}

// watchSupersededRejoin retries joining while this node is stuck replaced.
func (a *App) watchSupersededRejoin() {
	if a.isMember() || a.SupersededBy() == nil {
		return
	}
	a.retryPendingRejoin()
	if a.isMember() {
		return
	}
	for _, addr := range a.peerListenAddrs() {
		rv, ok := a.probeReach(addr)
		if !ok || rv.Role != "master" {
			continue
		}
		a.followMasterAsReplica(addr)
		if a.isMember() {
			return
		}
	}
}
