package app

// Every full server keeps the shared configuration but schedules, records and
// alerts locally at all times. Shared observations never gate local checks.
// Configuration authority changes only through manual promotion.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/backup"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

const (
	replicaDir    = "replica"
	replicaConfig = "master.conf" // the master's configuration, as last copied
	// legacyReplicaConfig is the copy's name before 2.6.
	legacyReplicaConfig = "master.bind"
)

// replicaState is what a replica runs while it cannot reach the master.
type replicaState struct {
	mu    sync.Mutex
	cfg   *config.Config // master configuration copy (nil: none yet)
	local bool           // checking on its own right now
	since time.Time      // when local checking started

	merging bool      // a merge push is running
	merged  time.Time // last merge push that went through
	copied  time.Time // when the copy was taken
}

// ReplicaStatus is shown on the node's own pages.
type ReplicaStatus struct {
	Replica  bool      `json:"replica"`
	Local    bool      `json:"local"`
	Since    time.Time `json:"since,omitzero"`
	Copied   time.Time `json:"copied,omitzero"`
	Monitors int       `json:"monitors"`
	Site     string    `json:"site,omitempty"`
}

// ReplicaStatus reports whether this server is a replica and whether it is
// checking on its own.
func (a *App) ReplicaStatus() ReplicaStatus {
	a.am.mu.Lock()
	member := a.am.member != nil
	a.am.mu.Unlock()
	site := a.site() // takes r.mu itself
	r := &a.replica
	r.mu.Lock()
	defer r.mu.Unlock()
	st := ReplicaStatus{Replica: member, Local: r.local, Since: r.since, Copied: r.copied, Site: site}
	if r.cfg != nil {
		st.Monitors = len(r.cfg.Monitors)
	}
	return st
}

// loadReplica reads the configuration copy kept from the last sync.
func (a *App) loadReplica() {
	p := a.dataPath(replicaDir, replicaConfig)
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	_, cfg, diags := config.Load(p, b, a.validators())
	if cfg == nil || config.HasErrors(diags) {
		a.log.Warn("the copy of the master's configuration is not usable", "errors", len(diags))
		return
	}
	fi, _ := os.Stat(p)
	a.replica.mu.Lock()
	a.replica.cfg = cfg
	a.replica.local = a.isMember()
	if fi != nil {
		a.replica.copied = fi.ModTime()
	}
	a.replica.mu.Unlock()
}

// keepReplicaCopy unpacks a fresh state archive: the master's configuration
// is kept for checking on its own, its secrets are re-encrypted into this
// server's secret store so its channels and checks keep working.
func (a *App) keepReplicaCopy(archive, key string) error {
	tmp, err := os.MkdirTemp(a.dataPath(standbyDir), ".unpack-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	cfgPath := filepath.Join(tmp, "monitors.conf")
	if _, err := backup.Restore(f, tmp, cfgPath, key, true); err != nil {
		return fmt.Errorf("unpack the master's state: %w", err)
	}
	if err := a.copySecrets(tmp); err != nil {
		return fmt.Errorf("copy notification configuration: %w", err)
	}
	if c, err := readCarry(tmp, a.now()); err != nil {
		a.log.Warn("could not read silences and changes from the master's state", "err", err)
	} else if cb, err := json.Marshal(c); err == nil {
		_ = os.MkdirAll(a.dataPath(replicaDir), 0o700)
		_ = os.WriteFile(a.dataPath(replicaDir, replicaCarryFile), cb, 0o600)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.dataPath(replicaDir), 0o700); err != nil {
		return err
	}
	dst := a.dataPath(replicaDir, replicaConfig)
	if err := os.WriteFile(dst+".tmp", b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(dst+".tmp", dst); err != nil {
		return err
	}
	a.loadReplica()
	a.replica.mu.Lock()
	local := a.replica.local
	a.replica.mu.Unlock()
	if local {
		a.Reapply()
	}
	return nil
}

func (a *App) copySecrets(dir string) error {
	kek, err := store.LoadOrCreateKey(dir)
	if err != nil {
		return err
	}
	st, err := store.Open(dir, store.Options{Log: a.log.With("component", "replica-copy"), Now: a.opts.Now})
	if err != nil {
		return err
	}
	defer st.Close()
	if err := a.store.CopyControlFrom(st, false); err != nil {
		return err
	}
	return copySecretStores(st.Secrets(kek), a.secrets)
}

func copySecretStores(src, dst *store.Secrets) error {
	list, err := src.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, si := range list {
		v, err := src.Get(si.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if cur, err := dst.Get(si.Name); err == nil && cur == v {
			continue
		}
		if err := dst.Put(si.Name, v, si.Scopes); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// watchReplica keeps checking independent of the configuration master's link.
func (a *App) watchReplica() {
	a.am.mu.Lock()
	ag := a.am.agent
	a.am.mu.Unlock()
	r := &a.replica
	r.mu.Lock()
	want := ag != nil && r.cfg != nil
	change := want != r.local
	r.local = want
	if change && want {
		r.since = a.now()
	}
	r.mu.Unlock()
	if want {
		a.feedPeers()
		a.maybePushMerge(false)
	}
	if change {
		a.Reapply()
	}
}

// localEngineConfig is what a replica runs on its own: the master's
// monitors, channels and policies, judged by this node alone. Monitors
// pinned to other nodes only are left to them.
func (a *App) localEngineConfig() (engine.Config, bool) {
	r := &a.replica
	r.mu.Lock()
	cfg, local := r.cfg, r.local
	r.mu.Unlock()
	if !local || cfg == nil {
		return engine.Config{}, false
	}
	self := a.nodeName()
	ec := a.engineConfig(cfg)
	ec.SourceNode = self
	// Peer reachability is informational; each target is checked locally.
	ec.Nodes, ec.Self = nil, self
	seenMaster := false
	for _, n := range cfg.Nodes {
		seenMaster = seenMaster || n.Name == cfg.MName
		if n.Name == self {
			continue
		}
		ec.Nodes = append(ec.Nodes, engine.NodeSpec{Name: n.Name, Title: n.Description, Region: n.Region, Policy: n.Policy, Timeout: n.NodeTimeout})
	}
	if !seenMaster && cfg.MName != self {
		ec.Nodes = append(ec.Nodes, engine.NodeSpec{Name: cfg.MName})
	}
	keep := ec.Monitors[:0]
	for _, m := range ec.Monitors {
		if len(m.Nodes) > 0 && !containsStr(m.Nodes, self) {
			continue
		}
		m.Nodes = nil
		keep = append(keep, m)
	}
	ec.Monitors = keep
	// Shared maintenance applies here; ACKs and silences remain source-local.
	ec.Maints = ec.Maints[:0:0]
	for _, mt := range cfg.Maints {
		ec.Maints = append(ec.Maints, engine.MaintSpec{Name: mt.Name, Title: mt.Title, Monitors: mt.Monitors, Schedule: mt.Schedule, Notify: mt.Notify})
	}
	ec.Maints = append(ec.Maints, a.carryWindows(func(sel string) []string { return changeMonitors(cfg, sel) })...)
	ec.Anchors, ec.LocalDetect = nil, true
	for _, n := range cfg.Nodes {
		if n.Name != self {
			continue
		}
		ec.LocalDetect = n.LocalDetect && !n.NoAnchors
		for _, x := range n.AnchorsLAN {
			ec.Anchors = append(ec.Anchors, engine.AnchorSpec{Kind: engine.AnchorLAN, Addr: x})
		}
		for _, x := range n.AnchorsWAN {
			ec.Anchors = append(ec.Anchors, engine.AnchorSpec{Kind: engine.AnchorWAN, Addr: x})
		}
	}
	return ec, true
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// nodeName is this server's node name in the cluster.
func (a *App) nodeName() string {
	a.am.mu.Lock()
	defer a.am.mu.Unlock()
	if a.am.member != nil {
		return a.am.member.Node
	}
	if c := a.applied.Load(); c != nil {
		return c.MName
	}
	return ""
}

// site names this server in messages by its node name, as the other sites'
// results in every message do. A single machine has no site.
func (a *App) site() string {
	self := a.nodeName()
	a.am.mu.Lock()
	member := a.am.member != nil
	a.am.mu.Unlock()
	if member {
		return self
	}
	cfg := a.applied.Load()
	if cfg == nil {
		return ""
	}
	for _, n := range cfg.Nodes {
		if n.Name != cfg.MName {
			return self // a cluster
		}
	}
	return ""
}

// runReplica runs the replica watch while ctx lives.
func (a *App) runReplica(ctx context.Context) {
	a.every(ctx, 2*time.Second, a.watchReplica)
}

// replicaSynced reports whether this server has its copy of the master's
// state, without which it checks nothing.
func (a *App) replicaSynced() bool {
	a.replica.mu.Lock()
	defer a.replica.mu.Unlock()
	return a.replica.cfg != nil
}
