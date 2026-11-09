package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// After a promotion: no record is lost and none is overwritten. The new
// master starts from the old master's copy and merges in what this site
// wrote on its own (kept aside by applyPromote). The old master, once it is
// told to step down, rejoins as a replica and sends what it wrote after the
// copy through the same merge; a configuration it kept changing is kept
// aside as diverged, never applied by itself.

const (
	ownDir          = "own"
	promotedFile    = "promoted.json"
	divergedPending = "diverged-pending.json"
)

type promoted struct {
	Self      string           `json:"self"`       // this node's name before
	OldMaster string           `json:"old_master"` // the previous master's MName (rejoin name)
	Term      uint64           `json:"term"`       // the new term
	PrevTerm  uint64           `json:"prev_term"`  // the term of the copy
	Marks     map[string]int64 `json:"marks,omitempty"`
	Base      store.Snapshot   `json:"base"` // the configuration version taken over with
	OwnMerged bool             `json:"own_merged,omitempty"`
}

func (a *App) loadPromoted() (promoted, bool) {
	var p promoted
	b, err := os.ReadFile(a.dataPath(promotedFile))
	if err != nil || json.Unmarshal(b, &p) != nil {
		return p, false
	}
	return p, true
}

func (a *App) savePromoted(p promoted) {
	b, _ := json.Marshal(p)
	_ = os.WriteFile(a.dataPath(promotedFile), b, 0o600)
}

// afterPromote runs at start right after the store opens.
func (a *App) afterPromote() {
	p, ok := a.loadPromoted()
	if !ok {
		return
	}
	if p.Marks == nil {
		// What the copy holds of the old master: its later rows come
		// through the merge.
		p.Marks = a.store.MaxIDs()
		p.Base, _ = a.store.SnapshotsAt(1 << 62)
		p.Base.Text = ""
		a.savePromoted(p)
	}
	if p.OwnMerged {
		return
	}
	own := a.dataPath(standbyDir, ownDir)
	if _, err := os.Stat(filepath.Join(own, "core.db")); err == nil && p.Self != "" {
		if err := a.mergeOwn(own, p); err != nil {
			a.log.Error("merging what this site wrote before taking over", "err", err)
			return // kept for the next start
		}
	}
	_ = os.RemoveAll(own)
	p.OwnMerged = true
	a.savePromoted(p)
}

func (a *App) mergeOwn(dir string, p promoted) error {
	old, err := store.Open(dir, store.Options{Log: a.log.With("component", "own-before-promote"), Now: a.opts.Now})
	if err != nil {
		return err
	}
	defer old.Close()
	total := map[string]int{}
	for i := 0; i < 1000; i++ {
		marks, err := a.store.MergeMarks(p.Self)
		if err != nil {
			return err
		}
		tables, more, err := old.ExportSince(marks)
		if err != nil {
			return err
		}
		if len(tables) == 0 {
			break
		}
		res, err := a.store.Merge(store.MergeBatch{Site: p.Self, Term: p.PrevTerm, Tables: tables})
		if err != nil {
			return err
		}
		for k, v := range res.Added {
			total[k] += v
		}
		if !more {
			break
		}
	}
	a.log.Info("records this site wrote before taking over merged in", "site", p.Self, "added", total)
	return nil
}

// prevSite is the name the replaced master rejoins under: its previous
// physical site name. Authority changes never rename an incident source.
func (a *App) prevSite(p promoted) string {
	name := p.OldMaster
	if name == "" || name == p.Self {
		c := a.applied.Load()
		if c == nil || c.MName == "" || c.MName == p.Self {
			return ""
		}
		name = c.MName
	}

	return name
}

// rejoinOffer fills a retire notice with a join code for the replaced
// master, and sets the marks its rows are merged from.
func (a *App) rejoinOffer(n *cluster.RetireNotice) {
	p, ok := a.loadPromoted()
	host, port, err := net.SplitHostPort(a.opts.Listen)
	if !ok || err != nil || host == "" || net.ParseIP(host).IsUnspecified() {
		return
	}
	site := a.prevSite(p)
	if site == "" {
		return
	}
	code, _, err := a.CreateJoinCode("https://" + net.JoinHostPort(host, port))
	if err != nil {
		return
	}
	if err := a.store.SetMergeMarks(site, p.Marks); err != nil {
		return
	}
	n.Join, n.Node = code, site
	n.Version, n.SHA256 = p.Base.Serial, p.Base.SHA256
}

// demote turns a replaced master into a replica of the new one.
func (a *App) demote(n cluster.RetireNotice) {
	info, err := cluster.DecodeJoin(n.Join)
	if err != nil || info.Fingerprint != a.pki.Fingerprint() {
		a.log.Warn("the new master's join offer is not for this cluster; staying stopped", "err", err)
		return
	}
	cur := a.Config()
	if n.SHA256 != "" && cur.SHA != n.SHA256 {
		// Changed here after the copy the new master took over with.
		d := store.DivergedConfig{At: a.now(), Term: a.Term(), Serial: cur.Serial, BaseSerial: n.Version, SHA256: cur.SHA, Text: cur.Text}
		b, _ := json.Marshal(d)
		_ = os.WriteFile(a.dataPath(divergedPending), b, 0o600)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := a.JoinCluster(ctx, JoinOptions{Code: n.Join, Node: n.Node, Title: n.Node, Author: "system"}); err != nil {
		a.log.Warn("could not rejoin the new master as a replica", "err", err)
		return
	}
	a.log.Warn("this replaced master now follows the new one as a replica", "node", n.Node)
	_ = a.store.Audit(auditEntry("cluster.demoted", "system", map[string]any{"node": n.Node, "term": n.Term}))
	a.Reapply()
	a.engine.ClearClusterRoleIncidents()
	a.emit("banner", nil)
}

// pushDiverged sends a kept diverged configuration to the master.
func (a *App) pushDiverged(ag *cluster.Agent) error {
	b, err := os.ReadFile(a.dataPath(divergedPending))
	if err != nil {
		return nil
	}
	var d store.DivergedConfig
	if json.Unmarshal(b, &d) != nil {
		_ = os.Remove(a.dataPath(divergedPending))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ag.Call(ctx, http.MethodPost, cluster.DivergedPath, d, nil); err != nil {
		return err
	}
	return os.Remove(a.dataPath(divergedPending))
}

// AddDiverged keeps a node's diverged configuration (on the master).
func (a *App) AddDiverged(site string, d store.DivergedConfig) error {
	d.Site, d.At = site, a.now()
	if strings.TrimSpace(d.Text) == "" {
		return nil
	}
	if err := a.store.AddDiverged(d); err != nil {
		return err
	}
	a.log.Warn("a replaced master had a different configuration; kept aside, not applied", "site", site, "version", d.Serial)
	_ = a.store.Audit(auditEntry("cluster.config_diverged", "system", map[string]any{"site": site, "version": d.Serial, "term": d.Term}))
	a.emit("banner", nil)
	return nil
}

// isMember reports whether this server joined a cluster (read from disk:
// callable with any lock held).
func (a *App) isMember() bool {
	_, err := os.Stat(a.dataPath(agentFile))
	return err == nil
}
