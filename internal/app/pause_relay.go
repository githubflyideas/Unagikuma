package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
)

// Pausing is configuration (disabled=yes), and a node of a cluster runs
// its master's configuration. So a node passes a pause or resume on to
// its master, which writes it; the change then reaches every node. The
// node has already checked the person's rights.

// SitePausePath is the master's endpoint for nodes' pause and resume.
const SitePausePath = "/api/v1/agent/pause"

// ErrMasterUnavailable: the master could not be reached to pass a change on.
var ErrMasterUnavailable = errors.New("the master cannot be reached right now; try again")

// PauseRequest pauses or resumes monitors on behalf of a person.
type PauseRequest struct {
	Monitors []string `json:"monitors"`
	Paused   bool     `json:"paused"`
	Actor    string   `json:"actor"`
}

func (p PauseRequest) valid() bool {
	return len(p.Monitors) > 0 && len(p.Monitors) <= 1000 && p.Actor != "" && len(p.Actor) <= 256
}

// SetPaused writes disabled=yes on the monitors' records, or removes it.
func SetPaused(f *config.File, names []string, paused bool) error {
	for _, name := range names {
		var rec config.RecordSpec
		found := false
		for i := range f.Stmts {
			st := &f.Stmts[i]
			if r, ok := config.StmtRecord(st); ok && r.Name == name {
				rec = config.RecordSpec{Name: r.Name, Interval: r.Interval, Type: r.Type, Target: r.Target, Opts: r.Opts, Comment: st.Comment}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("record %s not found", name)
		}
		var opts []config.Opt
		for _, o := range rec.Opts {
			if o.Key != "disabled" {
				opts = append(opts, o)
			}
		}
		if paused {
			opts = append(opts, config.Opt{Key: "disabled", Value: "yes"})
		}
		rec.Opts = opts
		if err := f.UpsertRecord(rec); err != nil {
			return err
		}
	}
	return nil
}

// IsMember reports whether this server is a node of a cluster, running
// its master's configuration.
func (a *App) IsMember() bool {
	a.am.mu.Lock()
	defer a.am.mu.Unlock()
	return a.am.member != nil
}

// RelayPause passes a pause or resume on to the master.
func (a *App) RelayPause(ctx context.Context, req PauseRequest) error {
	if !req.valid() {
		return fmt.Errorf("invalid pause request")
	}
	cfg := a.EffectiveConfig()
	if cfg == nil || cfg.MName == "" {
		return ErrMasterUnavailable
	}
	body, _ := json.Marshal(req)
	for _, p := range a.peerIdentities() {
		if p.Name != cfg.MName || p.Addr == "" || p.Fingerprint == "" {
			continue
		}
		u := url.URL{Scheme: "https", Host: strings.TrimPrefix(p.Addr, "https://"), Path: SitePausePath}
		hr, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		hr.Header.Set(peerSiteHeader, a.nodeName())
		hr.Header.Set("Content-Type", "application/json")
		client := a.peerClient(p)
		res, err := client.Do(hr)
		client.CloseIdleConnections()
		if err != nil {
			continue // another address of the master may answer
		}
		res.Body.Close()
		switch res.StatusCode {
		case http.StatusNoContent:
			return nil
		case http.StatusNotFound:
			return engine.ErrNotFound
		}
		return fmt.Errorf("%w (HTTP %d)", ErrMasterUnavailable, res.StatusCode)
	}
	return ErrMasterUnavailable
}

// ServeSitePause writes a node's pause or resume on the master.
func (a *App) ServeSitePause(w http.ResponseWriter, r *http.Request) {
	peer, ok := a.PeerRequester(r)
	if !ok {
		http.Error(w, "peer identity required", http.StatusUnauthorized)
		return
	}
	var req PauseRequest
	if json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req) != nil || !req.valid() {
		http.Error(w, "invalid pause request", http.StatusBadRequest)
		return
	}
	if a.editable() != nil {
		http.Error(w, "not the master", http.StatusConflict)
		return
	}
	cfg := a.Config().Config
	for _, n := range req.Monitors {
		if cfg.FindMonitor(n) == nil {
			http.Error(w, "monitor not found", http.StatusNotFound)
			return
		}
	}
	verb := "resume"
	if req.Paused {
		verb = "pause"
	}
	msg := fmt.Sprintf("%s %s (from node %s)", verb, strings.Join(req.Monitors, ", "), peer)
	if _, err := a.EditConfigFrom(req.Actor+"@"+peer, msg, map[string]any{"node": peer}, func(f *config.File) error {
		return SetPaused(f, req.Monitors, req.Paused)
	}); err != nil {
		http.Error(w, "pause failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	_ = a.store.Audit(auditEntry("monitor."+verb+"_from_node", req.Actor, map[string]any{"node": peer, "monitors": strings.Join(req.Monitors, ",")}))
	w.WriteHeader(http.StatusNoContent)
}
