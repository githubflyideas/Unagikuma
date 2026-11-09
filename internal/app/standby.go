package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/backup"
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Handing the master role to another server without an election: the
// master keeps standbys (NODE standby=https://…) supplied with a copy of
// its state — databases, monitors.conf, the secret key and the cluster CA
// — so a standby can take over by restoring it. Every handover raises the
// term; agents follow the highest term they have seen, and a master that
// learns of a higher term steps down, so two masters never run at once
// for long. Promotion is always somebody's decision (a button or
// `unagikuma promote`), never automatic: without a quorum protocol an
// automatic takeover could split the cluster when the network does.
const (
	termFile       = "cluster-term"
	supersededFile = "cluster-superseded.json"
	standbyDir     = "standby"
	stateFile      = "state.nkb"
	stateMetaFile  = "state.json"
	promoteFile    = "promote.json"
	previousFile   = "cluster-previous.json"
)

// StandbyState is the copy of the master's state a standby holds.
type StandbyState struct {
	Term    uint64    `json:"term"` // the master's term when it was taken
	Fetched time.Time `json:"fetched"`
	Size    int64     `json:"size"`
	Key     string    `json:"key"`
	Error   string    `json:"error,omitempty"` // last failed fetch
}

// Superseded records that this master was replaced.
type Superseded struct {
	Term uint64    `json:"term"`
	At   time.Time `json:"at"`
	By   string    `json:"by,omitempty"` // the node it was handed to, or that reported the newer master
}

func (a *App) dataPath(name ...string) string {
	return filepath.Join(append([]string{a.opts.DataDir}, name...)...)
}

// Term is this master's term: 0 until the first handover.
func (a *App) Term() uint64 {
	b, err := os.ReadFile(a.dataPath(termFile))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// SupersededBy reports whether this master was replaced.
func (a *App) SupersededBy() *Superseded {
	b, err := os.ReadFile(a.dataPath(supersededFile))
	if err != nil {
		return nil
	}
	var s Superseded
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return &s
}

// supersede retires configuration authority and refuses agents; local target
// checking continues. The authority handover survives restarts.
func (a *App) supersede(term uint64, by string) {
	if s := a.SupersededBy(); s != nil && s.Term >= term {
		return
	}
	s := Superseded{Term: term, At: a.now(), By: by}
	b, _ := json.Marshal(s)
	if err := os.WriteFile(a.dataPath(supersededFile), b, 0o644); err != nil {
		a.log.Error("cannot record the handover", "err", err)
	}
	a.log.Warn("configuration authority was transferred; local checks continue", "term", term, "by", by)
	_ = a.store.Audit(auditEntry("cluster.superseded", by, map[string]any{"term": term}))
	if c := a.applied.Load(); c != nil {
		a.applyConfig(c)
	}
	a.engine.ClearClusterRoleIncidents()
	a.master.Retire(fmt.Sprintf("this master was replaced (term %d)", term))
	a.emit("banner", nil)
}

// isStandby reports whether a node may hold the master's state.
func (a *App) isStandby(node string) bool {
	if c := a.applied.Load(); c != nil {
		if n := c.FindNode(node); n != nil {
			// A declared standby, or any server that joined (a replica).
			return n.Standby != "" || a.master.HasCap(node, cluster.CapReplica)
		}
	}
	return false
}

// writeState streams an encrypted state archive for a standby. The
// passphrase is new for each download and travels in a header over the
// pinned TLS session only.
func (a *App) writeState(w http.ResponseWriter) error {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	key := hex.EncodeToString(b[:])
	w.Header().Set(cluster.StateKeyHeader, key)
	w.Header().Set("Content-Type", "application/octet-stream")
	_, err := backup.Write(context.Background(), w, a.store.BackupDB, backup.Options{
		DataDir: a.opts.DataDir, ConfigPath: a.opts.ConfigPath, Passphrase: key,
		Instance: a.instance(), Version: a.opts.Version, Now: a.now(),
	})
	return err
}

// syncStandbys asks connected standbys to fetch the state.
func (a *App) syncStandbys() {
	c := a.applied.Load()
	if c == nil || a.SupersededBy() != nil {
		return
	}
	for _, n := range c.Nodes {
		if n.Name != c.MName && a.isStandby(n.Name) {
			a.master.NotifyState(n.Name)
		}
	}
}

// ErrNotStandby means a handover target is not a connected standby.
var ErrNotStandby = errors.New("the node is not a connected standby (NODE standby=https://…)")

// HandOver makes a standby the master: it fetches the current state and
// restarts as master with the next term, while this server steps down.
func (a *App) HandOver(node, author string) (uint64, error) {
	if !a.isStandby(node) {
		return 0, ErrNotStandby
	}
	connected := false
	for _, n := range a.master.Connected() {
		connected = connected || n == node
	}
	if !connected {
		return 0, ErrNotStandby
	}
	term := a.Term() + 1
	if !a.master.Promote(node, term) {
		return 0, ErrNotStandby
	}
	_ = a.store.Audit(auditEntry("cluster.handover", author, map[string]any{"node": node, "term": term}))
	// Step down once the standby had time to fetch the state; until then
	// this master keeps serving it.
	go func() {
		time.Sleep(a.handoverWait())
		a.supersede(term, node)
	}()
	return term, nil
}

func (a *App) handoverWait() time.Duration {
	if a.opts.HandoverWait > 0 {
		return a.opts.HandoverWait
	}
	return 20 * time.Second
}

// ---- standby side ----

// fetchState keeps a fresh copy of the master's state (standby nodes).
func (a *App) fetchState(ctx context.Context, ag *cluster.Agent) (StandbyState, error) {
	dir := a.dataPath(standbyDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return StandbyState{}, err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return StandbyState{}, err
	}
	defer os.Remove(tmp.Name())
	fctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	key, term, err := ag.FetchState(fctx, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	meta := a.Standby()
	if err != nil {
		meta.Error = err.Error()
		a.saveStandby(meta)
		return meta, err
	}
	fi, _ := os.Stat(tmp.Name())
	if err := os.Rename(tmp.Name(), filepath.Join(dir, stateFile)); err != nil {
		return meta, err
	}
	meta = StandbyState{Term: term, Fetched: a.now(), Size: fi.Size(), Key: key}
	a.saveStandby(meta)
	if err := a.keepReplicaCopy(filepath.Join(dir, stateFile), key); err != nil {
		a.log.Warn("could not keep the master's configuration for checking on its own", "err", err)
	}
	a.log.Info("master state copied", "term", term, "bytes", fi.Size())
	return meta, nil
}

func (a *App) saveStandby(m StandbyState) {
	b, _ := json.Marshal(m)
	_ = os.WriteFile(a.dataPath(standbyDir, stateMetaFile), b, 0o600)
}

// Standby is the state copy this server holds (zero when none).
func (a *App) Standby() StandbyState {
	var m StandbyState
	if b, err := os.ReadFile(a.dataPath(standbyDir, stateMetaFile)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// ErrNoState means there is no copy of the master's state to take over
// with.
var ErrNoState = errors.New("this server holds no copy of the master's state: it is not a standby, or has not fetched it yet")

// ErrPromoteRefused means a reachable master already holds term >= the
// term this promote would take (Rule 1 / BUG-11).
var ErrPromoteRefused = errors.New("another master is already running at an equal or higher term; promote refused")

// PromoteRefused carries the competing master for API/UI messages.
type PromoteRefused struct {
	Peer string
	Term uint64
}

func (e *PromoteRefused) Error() string {
	return ErrPromoteRefused.Error()
}

func (e *PromoteRefused) Unwrap() error { return ErrPromoteRefused }

// promoteDelay returns how long a manual promote waits before checking
// peers (random 1–10 s). Tests override promoteDelayFn.
var promoteDelayFn = func() time.Duration {
	var b [1]byte
	_, _ = rand.Read(b[:])
	return time.Duration(1+int(b[0])%10) * time.Second
}

// promoteRequest is left for the next start, which restores the state
// before opening anything.
type promoteRequest struct {
	Term uint64    `json:"term"`
	At   time.Time `json:"at"`
}

// Promote turns this standby into the master with term (0: the copy's term
// plus one) and asks for a restart. With a connection to the master the
// state is refreshed first.
//
// Manual promote (Rule 1 / BUG-11): wait a random 1–10 s, repeatedly asking
// every known peer and the master address list via Reach; if any reachable
// node reports a master with term >= the term this node would take, refuse.
// A handover from the current master (author "handover") skips the delay
// but still refuses a strictly newer foreign master.
func (a *App) Promote(ctx context.Context, term uint64, author string) (uint64, error) {
	a.am.mu.Lock()
	ag := a.am.agent
	a.am.mu.Unlock()
	if ag != nil && ag.Status().Connected {
		if _, err := a.fetchState(ctx, ag); err != nil {
			a.log.Warn("could not refresh the master's state; taking over with the last copy", "err", err)
		}
	}
	meta := a.Standby()
	if meta.Key == "" {
		return 0, ErrNoState
	}
	if _, err := os.Stat(a.dataPath(standbyDir, stateFile)); err != nil {
		return 0, ErrNoState
	}
	if term == 0 {
		term = meta.Term + 1
		if ag != nil {
			term = max(meta.Term, ag.Term()) + 1
		}
	}
	// Remember peer listens before restart so the new master and any
	// concurrent promoter can discover each other after heal.
	a.persistPeerListens()
	manual := author != "handover"
	if manual {
		if err := a.waitPromoteGuard(ctx, term); err != nil {
			return 0, err
		}
	}

	if peer, t, ok := a.competingMaster(term); ok {
		a.log.Warn("promote refused: another master is already running", "peer", peer, "term", t, "want", term)
		return 0, &PromoteRefused{Peer: peer, Term: t}
	}
	if err := writePromote(a.opts.DataDir, term, a.now()); err != nil {
		return 0, err
	}
	a.log.Warn("taking over as master: restarting", "term", term, "by", author)
	a.requestRestart()
	return term, nil
}

func writePromote(dir string, term uint64, now time.Time) error {
	b, _ := json.Marshal(promoteRequest{Term: term, At: now})
	return os.WriteFile(filepath.Join(dir, promoteFile), b, 0o600)
}

// PromoteOffline prepares a stopped standby to start as master (the
// promote command).
func PromoteOffline(dir string, now time.Time) (uint64, StandbyState, error) {
	var meta StandbyState
	b, err := os.ReadFile(filepath.Join(dir, standbyDir, stateMetaFile))
	if err != nil || json.Unmarshal(b, &meta) != nil || meta.Key == "" {
		return 0, meta, ErrNoState
	}
	term := meta.Term + 1
	if m, err := readMembershipTerm(dir); err == nil && m >= term {
		term = m + 1
	}
	a := &App{opts: Options{DataDir: dir, Now: time.Now}}
	a.loadMembership()
	pki, err := loadSitePKI(dir, now)
	if err != nil {
		return 0, meta, err
	}
	a.pki = pki
	if err := a.waitPromoteGuard(context.Background(), term); err != nil {
		return 0, meta, err
	}
	return term, meta, writePromote(dir, term, now)
}

func readMembershipTerm(dir string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(dir, agentFile))
	if err != nil {
		return 0, err
	}
	var m Membership
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	return m.Term, nil
}

// applyPromote runs at start, before anything is opened: a standby told
// to take over restores the master's state and becomes the master.
func applyPromote(dir, configPath string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, promoteFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var req promoteRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return false, fmt.Errorf("%s: %w", promoteFile, err)
	}
	var meta StandbyState
	mb, err := os.ReadFile(filepath.Join(dir, standbyDir, stateMetaFile))
	if err != nil || json.Unmarshal(mb, &meta) != nil {
		return false, ErrNoState
	}
	f, err := os.Open(filepath.Join(dir, standbyDir, stateFile))
	if err != nil {
		return false, ErrNoState
	}
	defer f.Close()
	// The master this standby followed: it is told to step down once it
	// can be reached, in case it was only cut off and comes back.
	var prev []string
	var self string
	if b, err := os.ReadFile(filepath.Join(dir, agentFile)); err == nil {
		var m Membership
		if json.Unmarshal(b, &m) == nil && m.Server != "" {
			prev = append(prev, m.Server)
			self = m.Node
		}
	}
	// Restore into isolation: a promotion transfers control, never runtime state.
	tmp, err := os.MkdirTemp(dir, ".promote-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	tmpConfig := filepath.Join(tmp, "config")
	if _, err := backup.Restore(f, tmp, tmpConfig, meta.Key, true); err != nil {
		return false, fmt.Errorf("unpack authority snapshot: %w", err)
	}
	src, err := store.Open(tmp, store.Options{Tool: true})
	if err != nil {
		return false, err
	}
	defer src.Close()
	dst, err := store.Open(dir, store.Options{Tool: true})
	if err != nil {
		return false, err
	}
	srcKey, err := store.LoadOrCreateKey(tmp)
	if err != nil {
		dst.Close()
		return false, err
	}
	dstKey, err := store.LoadOrCreateKey(dir)
	if err != nil {
		dst.Close()
		return false, err
	}
	if err = dst.CopyControlFrom(src, true); err == nil {
		err = copySecretStores(src.Secrets(srcKey), dst.Secrets(dstKey))
	}
	dst.Close()
	if err != nil {
		return false, err
	}
	for _, name := range []string{"cluster-ca.crt", "cluster-ca.key", "cluster.crt", "cluster.key"} {
		b, err := os.ReadFile(filepath.Join(tmp, name))
		if err != nil {
			return false, err
		}
		if err := writePeerAtomic(filepath.Join(dir, name), b); err != nil {
			return false, err
		}
	}
	cfg, err := os.ReadFile(tmpConfig)
	if err != nil {
		return false, err
	}
	if err := writePeerAtomic(configPath, cfg); err != nil {
		return false, err
	}
	markPromotedClean(dir)
	oldMaster, err := adoptPromotedConfig(configPath, self)
	if err != nil {
		return false, fmt.Errorf("adopt the configuration as this node: %w", err)
	}
	base, _ := src.SnapshotsAt(1 << 62)
	base.Text = ""
	pb, _ := json.Marshal(promoted{Self: self, OldMaster: oldMaster, Term: req.Term, PrevTerm: meta.Term, Base: base, OwnMerged: true, Marks: map[string]int64{}})
	if err := os.WriteFile(filepath.Join(dir, promotedFile), pb, 0o600); err != nil {
		return false, err
	}
	for _, name := range []string{agentFile, agentTokenFile, supersededFile, promoteFile} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	if len(prev) > 0 {
		b, _ := json.Marshal(prev)
		_ = os.WriteFile(filepath.Join(dir, previousFile), b, 0o644)
	}
	return true, os.WriteFile(filepath.Join(dir, termFile), []byte(strconv.FormatUint(req.Term, 10)+"\n"), 0o644)
}

// markPromotedClean records that a promote restart is a clean handover, not
// a crash of the previous process that owned this data directory.
func markPromotedClean(dir string) {
	st, err := store.Open(dir, store.Options{Tool: true})
	if err != nil {
		return
	}
	defer st.Close()
	_ = st.SetSetting("clean_shutdown", "1")
	_ = st.SetSetting("last_beat", "")
}

// adoptPromotedConfig makes the promoting node the master in the restored
// configuration (master=<self>) so its own NODE record is no longer treated
// as a missing remote agent. The previous master's name is returned so it
// can rejoin under that name.
func adoptPromotedConfig(configPath, self string) (string, error) {
	if self == "" {
		return "", nil
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	f, diags := config.Parse(configPath, b)
	if config.HasErrors(diags) {
		return "", fmt.Errorf("parse: %v", diags[0])
	}
	_, cfg, diags := config.Load(configPath, b, config.Options{})
	if cfg == nil {
		return "", fmt.Errorf("load: %v", diags)
	}
	old := cfg.MName
	if old == self {
		return old, nil
	}
	// The previous master is still a physical monitoring site. Keep it in
	// the configuration before changing authority, including old nodes= scopes.
	exists := false
	for _, n := range cfg.Nodes {
		if n.Name == old {
			exists = true
		}
	}
	if !exists {
		if cfg.HasName(old) {
			return "", fmt.Errorf("previous site name %q conflicts with another record", old)
		}
		if err := f.UpsertRecord(config.RecordSpec{Name: old, Type: "NODE", Target: old}); err != nil {
			return "", err
		}
	}
	if err := f.SetMaster(self); err != nil {
		return "", err
	}
	if _, _, diags := config.Load(configPath, f.Bytes(), config.Options{}); config.HasErrors(diags) {
		return "", fmt.Errorf("promoted configuration: %v", diags)
	}
	cur, err := f.Serial()
	if err != nil {
		return "", err
	}
	next, _, _ := config.NextSerial(cur, time.Now().UTC())
	if err := f.SetSerial(next); err != nil {
		return "", err
	}
	if err := os.WriteFile(configPath, f.Bytes(), 0o600); err != nil {
		return "", err
	}
	return old, nil
}

// requestRestart asks the process to restart (the server command re-runs
// itself once Run returns).
func (a *App) requestRestart() {
	a.restartOnce.Do(func() { close(a.restart) })
}

// RestartRequested is closed when the app needs a restart.
func (a *App) RestartRequested() <-chan struct{} { return a.restart }

func auditEntry(action, actor string, detail map[string]any) store.AuditEntry {
	return store.AuditEntry{Action: action, Actor: actor, Target: "cluster", Detail: detail}
}

// ErrSuperseded refuses configuration changes on a master that handed
// over: they belong on the new master.
var ErrSuperseded = errors.New("this server handed the master role to another one; change the configuration there")

// retirePrevious tells the master this one replaced to step down, until
// it confirms: the former master retains local checks but no longer
// accepts configuration changes or claims configuration authority.
func (a *App) retirePrevious(ctx context.Context) {
	b, err := os.ReadFile(a.dataPath(previousFile))
	if err != nil || a.SupersededBy() != nil {
		return
	}
	var prev []string
	if json.Unmarshal(b, &prev) != nil {
		return
	}
	n, err := a.pki.RetireNotice(a.Term(), a.now())
	if err != nil {
		return
	}
	a.rejoinOffer(&n)
	// Also tell every known peer listen address (BUG-10): replicas learn the
	// successor even while the old master is still unreachable.
	seen := map[string]bool{}
	for _, srv := range prev {
		seen[srv] = true
	}
	for _, host := range a.peerListenAddrs() {
		u := "https://" + host
		if !seen[u] && !seen[host] {
			prev = append(prev, u)
			seen[u] = true
		}
	}
	var left []string
	for _, srv := range prev {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := cluster.SendRetire(sctx, srv, a.pki.Fingerprint(), n)
		cancel()
		if err != nil {
			a.log.Debug("previous master not told yet", "server", srv, "err", err)
			left = append(left, srv)
			continue
		}
		a.log.Info("the previous master acknowledged the handover", "server", srv)
	}
	if len(left) == 0 {
		_ = os.Remove(a.dataPath(previousFile))
		return
	}
	b, _ = json.Marshal(left)
	_ = os.WriteFile(a.dataPath(previousFile), b, 0o644)
}

// AcceptRetire handles a notice from the master that replaced this one.
func (a *App) AcceptRetire(n cluster.RetireNotice) error {
	if err := a.pki.CheckRetire(n); err != nil {
		return err
	}
	if n.Term <= a.Term() {
		return fmt.Errorf("term %d is not newer than this master's %d", n.Term, a.Term())
	}
	a.supersede(n.Term, "the new master")
	if n.Join != "" {
		go a.demote(n)
	}
	return nil
}

// Both the live API and the stopped-node CLI use the same manual guard.
func (a *App) waitPromoteGuard(ctx context.Context, term uint64) error {
	deadline := time.Now().Add(promoteDelayFn())
	for {
		if peer, t, ok := a.competingMaster(term); ok {
			return &PromoteRefused{Peer: peer, Term: t}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		timer := time.NewTimer(min(left, time.Second))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
