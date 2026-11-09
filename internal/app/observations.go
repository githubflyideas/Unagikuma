package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/observation"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const SiteObservationPath = "/api/v1/agent/observations"
const peerSiteHeader = "X-Unagikuma-Site"

func (a *App) configuredSite(name string) bool {
	c := a.EffectiveConfig()
	return c != nil && (name == c.MName || c.FindNode(name) != nil)
}

// PeerRequester requires possession of the announced, independent site's TLS key.
func (a *App) PeerRequester(r *http.Request) (string, bool) {
	node := r.Header.Get(peerSiteHeader)
	if r.TLS == nil || node == "" || !a.configuredSite(node) {
		return "", false
	}
	for _, p := range a.peerIdentities() {
		if p.Name == node && p.Fingerprint != "" && cluster.VerifyPeerIdentity(*r.TLS, p.Fingerprint) == nil {
			return node, true
		}
	}
	return "", false
}
func (a *App) ServeSiteObservations(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.PeerRequester(r); !ok {
		http.Error(w, "peer identity required", http.StatusUnauthorized)
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	d, err := a.store.SiteObservationDelta(a.nodeName(), r.URL.Query().Get("epoch"), after)
	if err != nil {
		http.Error(w, "observation storage unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(d)
}
func (a *App) peerClient(p cluster.PeerAddr) *http.Client {
	cfg := cluster.PinnedPeerTLSConfig(p.Fingerprint)
	cfg.Certificates = []tls.Certificate{a.pki.PeerIdentity().Leaf}
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: cfg}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (a *App) pullSiteObservations(ctx context.Context, p cluster.PeerAddr) error {
	for page := 0; page < 64; page++ {
		more, err := a.pullSiteObservationPage(ctx, p)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}
func (a *App) pullSiteObservationPage(ctx context.Context, p cluster.PeerAddr) (bool, error) {
	epoch, seq := a.store.SiteObservationCursor(p.Name)
	u := url.URL{Scheme: "https", Host: strings.TrimPrefix(p.Addr, "https://"), Path: SiteObservationPath}
	q := u.Query()
	q.Set("epoch", epoch)
	q.Set("after", strconv.FormatInt(seq, 10))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set(peerSiteHeader, a.nodeName())
	client := a.peerClient(p)
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false, fmt.Errorf("peer observations: HTTP %d", res.StatusCode)
	}
	var d observation.Delta
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&d); err != nil {
		return false, err
	}
	filtered := d.Rows[:0]
	cfg := a.EffectiveConfig()
	for _, row := range d.Rows {
		if m := cfg.FindMonitor(row.MonitorID); m != nil && m.DefHash == row.DefHash {
			filtered = append(filtered, row)
		}
	}
	d.Rows = filtered
	if err := a.store.ApplySiteObservationDelta(p.Name, d); err != nil {
		return false, err
	}
	if len(d.Rows) > 0 {
		a.emit("state", nil)
	}
	return d.More, nil
}
func (a *App) syncSiteObservations(ctx context.Context) {
	self := a.nodeName()
	byName := map[string]cluster.PeerAddr{}
	for _, p := range a.peerIdentities() {
		if p.Name != self && p.Addr != "" && p.Fingerprint != "" && a.configuredSite(p.Name) {
			byName[p.Name] = p
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, p := range byName {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func(p cluster.PeerAddr) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := a.pullSiteObservations(ctx, p); err != nil {
				a.log.Debug("site observation sync", "site", p.Name, "err", err)
			}
		}(p)
	}
	wg.Wait()
}
func (a *App) runSiteObservations(ctx context.Context) {
	a.every(ctx, 2*time.Second, func() { a.syncSiteObservations(ctx) })
}
func (a *App) ObservationSnapshot(name string) *observation.Snapshot {
	cfg := a.EffectiveConfig()
	if cfg == nil || len(cfg.Nodes) == 0 {
		return nil
	}
	m := cfg.FindMonitor(name)
	if m == nil {
		return nil
	}
	var probers []string
	selfProbes := true
	for _, n := range cfg.Nodes {
		if n.Name == cfg.MName {
			selfProbes = n.Probe
		} else if n.Probe {
			probers = append(probers, n.Name)
		}
	}
	sites := participants(m.Nodes, cfg.MName, selfProbes, probers)
	snap, err := a.store.SiteObservationSnapshot(name, m.DefHash, sites, a.now())
	if err != nil {
		return nil
	}
	if snap != nil {
		h := a.engine.Health()
		snap.Gateway = h.GatewayState
		snap.Uplink = h.Uplink
	}
	return snap
}
