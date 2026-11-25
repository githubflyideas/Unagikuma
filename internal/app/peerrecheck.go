package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

// Peer rechecks. When a monitor that other sites check too fails here for
// the first time — before the failure is confirmed — those sites are asked
// to check it at once. By the time the failure is confirmed here (after
// its retries) their answers have usually arrived with the regular
// observation sync, so the alert can say whether they see it too.
//
// Nothing here may ever delay or stop an alert, and it must not turn into
// a storm of checks:
//   - the engine only drops a request into a buffered queue; when the queue
//     is full the request is dropped, never waited for;
//   - requests made within peerRecheckBatch go to each site together, at
//     most peerRecheckMaxItems monitors in one request;
//   - one monitor asks again only after peerRecheckCooldown, so a flapping
//     link does not keep the other sites busy;
//   - a site that cannot be reached within peerRecheckTimeout is given up
//     on; the alert then shows that site's last word from before;
//   - the receiving site checks only monitors it checks itself, defined the
//     same way, through the engine's own rate-limited "check now", and only
//     for a site that proves its identity.

const (
	// PeerRecheckPath is where a site asks another to check monitors now.
	PeerRecheckPath     = "/api/v1/agent/recheck"
	peerRecheckBatch    = time.Second
	peerRecheckCooldown = 5 * time.Minute
	peerRecheckTimeout  = 2 * time.Second
	peerRecheckMaxItems = 500
	peerRecheckQueue    = 4096
)

// RecheckItem is one monitor to check now, with its definition's hash so
// that a site whose definition differs does not check something else.
type RecheckItem struct {
	Name    string `json:"name"`
	DefHash string `json:"def_hash"`
}

type recheckRequest struct {
	item  RecheckItem
	sites []string
}

type peerRechecker struct {
	q    chan recheckRequest
	mu   sync.Mutex
	last map[string]time.Time // monitor -> when it last asked
}

func newPeerRechecker() *peerRechecker {
	return &peerRechecker{q: make(chan recheckRequest, peerRecheckQueue), last: map[string]time.Time{}}
}

// firstFailure is the engine's FirstFailure hook: it never blocks.
func (a *App) firstFailure(monitor, defHash string, sites []string) {
	select {
	case a.rechecks.q <- recheckRequest{item: RecheckItem{Name: monitor, DefHash: defHash}, sites: sites}:
	default:
		a.log.Debug("peer recheck queue full: request dropped", "monitor", monitor)
	}
}

// runPeerRechecks sends the queued requests, in batches.
func (a *App) runPeerRechecks(ctx context.Context) {
	for {
		var first recheckRequest
		select {
		case <-ctx.Done():
			return
		case first = <-a.rechecks.q:
		}
		batch := []recheckRequest{first}
		timer := time.NewTimer(peerRecheckBatch)
	collect:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case r := <-a.rechecks.q:
				batch = append(batch, r)
			case <-timer.C:
				break collect
			}
		}
		a.sendPeerRechecks(ctx, batch)
	}
}

// sendPeerRechecks groups a batch by site and asks each site once.
func (a *App) sendPeerRechecks(ctx context.Context, batch []recheckRequest) {
	now := a.now()
	self := a.nodeName()
	bySite := map[string][]RecheckItem{}
	a.rechecks.mu.Lock()
	for _, r := range batch {
		if t, ok := a.rechecks.last[r.item.Name]; ok && now.Sub(t) < peerRecheckCooldown {
			continue
		}
		a.rechecks.last[r.item.Name] = now
		for _, s := range r.sites {
			if s != self && len(bySite[s]) < peerRecheckMaxItems {
				bySite[s] = append(bySite[s], r.item)
			}
		}
	}
	for name, t := range a.rechecks.last {
		if now.Sub(t) >= peerRecheckCooldown {
			delete(a.rechecks.last, name)
		}
	}
	a.rechecks.mu.Unlock()
	if len(bySite) == 0 {
		return
	}
	peers := map[string]cluster.PeerAddr{}
	for _, p := range a.peerIdentities() {
		if p.Name != self && p.Addr != "" && p.Fingerprint != "" && a.configuredSite(p.Name) {
			peers[p.Name] = p
		}
	}
	var wg sync.WaitGroup
	for site, items := range bySite {
		p, ok := peers[site]
		if !ok {
			continue // not reachable as a peer: its last word is all there is
		}
		wg.Add(1)
		go func(p cluster.PeerAddr, items []RecheckItem) {
			defer wg.Done()
			if err := a.askPeerRecheck(ctx, p, items); err != nil {
				a.log.Debug("peer recheck not sent", "site", p.Name, "monitors", len(items), "err", err)
			}
		}(p, items)
	}
	wg.Wait()
}

func (a *App) askPeerRecheck(ctx context.Context, p cluster.PeerAddr, items []RecheckItem) error {
	body, err := json.Marshal(items)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, peerRecheckTimeout)
	defer cancel()
	u := url.URL{Scheme: "https", Host: strings.TrimPrefix(p.Addr, "https://"), Path: PeerRecheckPath}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set(peerSiteHeader, a.nodeName())
	req.Header.Set("Content-Type", "application/json")
	client := a.peerClient(p)
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", res.StatusCode) // an older site answers 404
	}
	return nil
}

// ServePeerRecheck checks now the monitors another site asks about: only
// for a site that proves its identity, only monitors this site checks
// itself and defines the same way, each through the engine's rate-limited
// "check now".
func (a *App) ServePeerRecheck(w http.ResponseWriter, r *http.Request) {
	peer, ok := a.PeerRequester(r)
	if !ok {
		http.Error(w, "peer identity required", http.StatusUnauthorized)
		return
	}
	if !a.engine.Running() {
		// Not monitoring yet (starting, or repair mode): asking the engine
		// would wait for a loop that is not there.
		http.Error(w, "not monitoring", http.StatusServiceUnavailable)
		return
	}
	var items []RecheckItem
	if json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&items) != nil {
		http.Error(w, "invalid recheck request", http.StatusBadRequest)
		return
	}
	if len(items) > peerRecheckMaxItems {
		items = items[:peerRecheckMaxItems]
	}
	cfg := a.EffectiveConfig()
	started := []string{}
	for _, it := range items {
		m := cfg.FindMonitor(it.Name)
		if m == nil || m.DefHash != it.DefHash {
			continue // unknown here, or defined differently: not the same check
		}
		if _, ok := a.engine.Row(it.Name); !ok {
			continue // not checked by this site
		}
		if a.engine.CheckNow(it.Name) == nil {
			started = append(started, it.Name)
		}
	}
	sort.Strings(started)
	if len(started) > 0 {
		a.log.Info("checking now at another site's request", "site", peer, "monitors", len(started))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"started": len(started)})
}
