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
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

const SiteIncidentActionPath = "/api/v1/agent/incident-action"

var ErrSourceUnavailable = errors.New("incident source unavailable")

type IncidentAction struct {
	GlobalID  string `json:"global_id"`
	Action    string `json:"action"`
	Actor     string `json:"actor"`
	Comment   string `json:"comment,omitempty"`
	DurationS int    `json:"duration_s,omitempty"`
}

func (op IncidentAction) valid() bool {
	return op.Actor != "" && len(op.Actor) <= 256 && len(op.Comment) <= 8192 && op.DurationS >= 0 && op.DurationS <= 366*86400 && (op.Action == "ack" || op.Action == "unack" || op.Action == "silence")
}

// ActOnIncident always resolves the source namespace, never the imported numeric ID.
func (a *App) ActOnIncident(ctx context.Context, id int64, op IncidentAction, via string) error {
	if !op.valid() {
		return fmt.Errorf("invalid incident action")
	}
	row, err := a.store.Incident(id)
	if err != nil {
		return err
	}
	if row == nil {
		return engine.ErrNotFound
	}
	op.GlobalID = row.GlobalID
	if row.Site == "" {
		return a.actLocalIncident(id, op, via)
	}
	for _, p := range a.peerIdentities() {
		if p.Name != row.SourceNode || p.Addr == "" || p.Fingerprint == "" || !a.configuredSite(p.Name) {
			continue
		}
		body, _ := json.Marshal(op)
		u := url.URL{Scheme: "https", Host: strings.TrimPrefix(p.Addr, "https://"), Path: SiteIncidentActionPath}
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
			return fmt.Errorf("%w: %s", ErrSourceUnavailable, p.Name)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return engine.ErrNotFound
		}
		if res.StatusCode != http.StatusNoContent {
			return fmt.Errorf("%w: HTTP %d", ErrSourceUnavailable, res.StatusCode)
		}
		return nil
	}
	return ErrSourceUnavailable
}
func (a *App) actLocalIncident(id int64, op IncidentAction, via string) error {
	var err error
	switch op.Action {
	case "ack":
		err = a.Engine().Ack(id, op.Actor, op.Comment, time.Duration(op.DurationS)*time.Second)
	case "unack":
		err = a.Engine().Unack(id, op.Actor)
	case "silence":
		until := time.Time{}
		if op.DurationS > 0 {
			until = time.Now().Add(time.Duration(op.DurationS) * time.Second)
		}
		err = a.Engine().Silence(id, op.Actor, until)
	default:
		return fmt.Errorf("invalid incident action")
	}
	if err != nil {
		return err
	}
	if err = a.store.AddAck(id, store.AckRecord{At: time.Now(), By: op.Actor, Via: via, Action: op.Action, Comment: op.Comment}); err != nil {
		return err
	}
	return a.store.Audit(auditEntry("incident."+op.Action, op.Actor, map[string]any{"global_id": op.GlobalID, "via": via, "comment": op.Comment}))
}
func (a *App) ServeSiteIncidentAction(w http.ResponseWriter, r *http.Request) {
	peer, ok := a.PeerRequester(r)
	if !ok {
		http.Error(w, "peer identity required", http.StatusUnauthorized)
		return
	}
	var op IncidentAction
	if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&op) != nil || !op.valid() {
		http.Error(w, "invalid incident action", http.StatusBadRequest)
		return
	}
	id, err := a.store.LocalIncidentID(op.GlobalID)
	if err != nil || id == 0 {
		http.Error(w, "incident not found", http.StatusNotFound)
		return
	}
	if err = a.actLocalIncident(id, op, "peer:"+peer); err != nil {
		code := 503
		if errors.Is(err, engine.ErrNotFound) {
			code = 404
		}
		http.Error(w, "incident action failed", code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// IsPushReceiver checks the physical site, independently of configuration authority.
func (a *App) IsPushReceiver(name string) bool {
	c := a.EffectiveConfig()
	if c == nil {
		return false
	}
	m := c.FindMonitor(name)
	return m != nil && m.Type == "PUSH" && len(m.Nodes) == 1 && m.Nodes[0] == a.nodeName()
}
