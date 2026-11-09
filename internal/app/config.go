package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Banner is a configuration status shown on every page.
type Banner struct {
	Kind    string              `json:"kind"`    // error | warning | info
	Code    string              `json:"code"`    // serial_not_bumped | serial_lower | invalid | applied
	Message string              `json:"message"` // English fallback; the UI translates by code
	Serial  uint32              `json:"serial,omitempty"`
	Diags   []config.Diagnostic `json:"diagnostics,omitempty"`
}

// ConfigState is the active configuration.
type ConfigState struct {
	Text   string
	SHA    string
	Serial uint32
	File   *config.File
	Config *config.Config
}

// ETag identifies a configuration version for optimistic concurrency.
func (c ConfigState) ETag() string { return fmt.Sprintf(`"%d-%s"`, c.Serial, c.SHA[:12]) }

// ErrConflict means the file changed since the caller read it.
var ErrConflict = errors.New("configuration changed in the meantime")

// ErrReadonly means the configuration file cannot be written.
var ErrReadonly = errors.New("configuration file is read-only")

// InvalidError carries diagnostics of a rejected configuration.
type InvalidError struct{ Diags []config.Diagnostic }

func (e *InvalidError) Error() string {
	if len(e.Diags) > 0 {
		return e.Diags[0].Error()
	}
	return "invalid configuration"
}

type configManager struct {
	app    *App
	path   string
	mu     sync.Mutex
	cur    ConfigState
	banner *Banner
	stat   fileStamp
}

type fileStamp struct {
	size int64
	mod  time.Time
}

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Starter is the monitors.conf written on first run: the header only;
// monitors come from the setup wizard, the editor or an import.
func Starter(now time.Time) []byte { return starter(now) }

func starter(now time.Time) []byte {
	serial, _, _ := config.NextSerial(0, now)
	return []byte(fmt.Sprintf(`; unagikuma monitors.conf
; Edit here or in the Web UI. After editing by hand, increase the revision
; below and unagikuma applies the change by itself. interval, retry,
; timeout and attempts are the defaults for every monitor.
$FILE revision=%d master=unagikuma interval=1m retry=15s timeout=5s attempts=3
`, serial))
}

// load reads the file at startup. A missing file is restored from the last
// snapshot, or created from the starter template; an invalid file keeps the
// last good snapshot running and raises a banner.
func (cm *configManager) load() error {
	text, err := os.ReadFile(cm.path)
	if errors.Is(err, os.ErrNotExist) {
		if snaps, _ := cm.app.store.Snapshots(1); len(snaps) > 0 {
			sn, _ := cm.app.store.SnapshotText(snaps[0].Serial)
			text = []byte(sn.Text)
		} else {
			text = starter(cm.app.now())
		}
		if err := writeAtomic(cm.path, text); err != nil {
			return fmt.Errorf("create %s: %w", cm.path, err)
		}
	} else if err != nil {
		return err
	}
	f, cfg, diags := config.Load(cm.path, text, cm.app.validators())
	if config.HasErrors(diags) {
		snaps, _ := cm.app.store.Snapshots(1)
		if len(snaps) == 0 {
			return &InvalidError{Diags: diags}
		}
		sn, _ := cm.app.store.SnapshotText(snaps[0].Serial)
		f2, cfg2, d2 := config.Load(cm.path, []byte(sn.Text), cm.app.validators())
		if config.HasErrors(d2) {
			return &InvalidError{Diags: diags}
		}
		cm.cur = ConfigState{Text: sn.Text, SHA: shaOf([]byte(sn.Text)), Serial: cfg2.Serial, File: f2, Config: cfg2}
		cm.banner = &Banner{Kind: "error", Code: "invalid", Message: "monitors.conf has errors; running the last good version", Diags: diags}
		cm.touchStamp()
		return nil
	}
	cm.cur = ConfigState{Text: string(text), SHA: shaOf(text), Serial: cfg.Serial, File: f, Config: cfg}
	cm.touchStamp()
	return cm.snapshot("startup", "", "")
}

func (cm *configManager) touchStamp() {
	if fi, err := os.Stat(cm.path); err == nil {
		cm.stat = fileStamp{fi.Size(), fi.ModTime()}
	}
}

func (cm *configManager) snapshot(source, author, message string) error {
	prev, _ := cm.app.store.Snapshots(1)
	summary := map[string]int{}
	if len(prev) > 0 && prev[0].Serial != cm.cur.Serial {
		if old, err := cm.app.store.SnapshotText(prev[0].Serial); err == nil {
			if _, oc, d := config.Load(cm.path, []byte(old.Text), config.Options{}); !config.HasErrors(d) {
				s := config.Summarize(config.Diff(oc, cm.cur.Config))
				summary = map[string]int{"added": s.Added, "removed": s.Removed, "changed": s.Changed}
			}
		}
	}
	return cm.app.store.SaveSnapshot(store.Snapshot{Serial: cm.cur.Serial, At: cm.app.now(), Source: source, Author: author,
		Message: message, Text: cm.cur.Text, SHA256: cm.cur.SHA, Summary: summary, Term: cm.app.Term()}, cm.cur.Config.Tunables.Int("retention.snapshots"))
}

// current returns the active configuration.
func (cm *configManager) current() ConfigState {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.cur
}

// apply installs a parsed configuration (lock held).
func (cm *configManager) apply(text []byte, f *config.File, cfg *config.Config, source, author, message string, renames map[string]string, client map[string]any) error {
	old := cm.cur
	if old.Config != nil {
		cm.app.renameMonitors(old.Config, cfg, renames)
	}
	cm.cur = ConfigState{Text: string(text), SHA: shaOf(text), Serial: cfg.Serial, File: f, Config: cfg}
	if err := cm.snapshot(source, author, message); err != nil {
		cm.app.log.Error("config snapshot", "err", err)
	}
	cm.app.applyConfig(cfg)
	var summary config.Summary
	if old.Config != nil {
		changes := config.Diff(old.Config, cfg)
		summary = config.Summarize(changes)
		cm.audit(old, cm.cur, changes, source, author, message, client)
	}
	cm.banner = nil
	cm.app.emit("serial", map[string]any{"serial": cfg.Serial, "source": source, "message": message,
		"summary": map[string]int{"added": summary.Added, "removed": summary.Removed, "changed": summary.Changed}})
	cm.app.emit("banner", nil)
	return nil
}

// Save validates and applies new text written from the UI or API. The
// serial is bumped automatically when the caller did not raise it.
func (cm *configManager) save(text []byte, ifMatch, author, message string, client map[string]any) (ConfigState, error) {
	return cm.saveRenamed(text, ifMatch, author, message, nil, client)
}

// saveRenamed is save for an edit that renamed monitors (old → new).
// client is the sign-in and browser a change from the web or the API came
// from (nil otherwise), for the audit log.
func (cm *configManager) saveRenamed(text []byte, ifMatch, author, message string, renames map[string]string, client map[string]any) (ConfigState, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if ifMatch != "" && ifMatch != cm.cur.ETag() {
		return cm.cur, ErrConflict
	}
	f, diags := config.Parse(cm.path, text)
	if config.HasErrors(diags) {
		return cm.cur, &InvalidError{Diags: diags}
	}
	if serial, err := f.Serial(); err == nil && serial <= cm.cur.Serial {
		next, warn, err := config.NextSerial(cm.cur.Serial, cm.app.now())
		if err != nil {
			return cm.cur, err
		}
		if warn != "" {
			cm.app.log.Warn("serial", "warning", warn)
		}
		if err := f.SetSerial(next); err != nil {
			return cm.cur, err
		}
	}
	text = f.Bytes()
	f, cfg, diags := config.Load(cm.path, text, cm.app.validators())
	if config.HasErrors(diags) {
		return cm.cur, &InvalidError{Diags: diags}
	}
	if err := config.CheckSerial(cfg.Serial, cm.cur.Serial); err != nil {
		return cm.cur, &InvalidError{Diags: []config.Diagnostic{{Pos: config.Pos{Line: 1, Col: 1}, Severity: config.SevError, Code: "invalid_serial", Msg: err.Error()}}}
	}
	if err := writeAtomic(cm.path, text); err != nil {
		if os.IsPermission(err) {
			return cm.cur, ErrReadonly
		}
		return cm.cur, err
	}
	cm.touchStamp()
	return cm.cur, cm.apply(text, f, cfg, "web", author, message, renames, client)
}

// edit applies a structured change to the current file.
// The change runs on the current text; if another save lands in between,
// it runs again on the newer text, so two people editing different
// records both succeed (a change can still refuse, e.g. a stale If-Match).
func (cm *configManager) edit(author, message string, client map[string]any, change func(f *config.File) error) (ConfigState, error) {
	for attempt := 0; ; attempt++ {
		cur := cm.current()
		f, _ := config.Parse(cm.path, []byte(cur.Text))
		if err := change(f); err != nil {
			return cur, err
		}
		st, err := cm.saveRenamed(f.Bytes(), cur.ETag(), author, message, f.Renamed, client)
		if errors.Is(err, ErrConflict) && attempt < 3 {
			continue
		}
		if err == nil {
			st = cm.current()
		}
		return st, err
	}
}

// check polls the file and applies external edits under the serial rules.
func (cm *configManager) check() {
	fi, err := os.Stat(cm.path)
	if err != nil {
		return
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	// File times are coarse (a few ms on many file systems): a same-size
	// edit right after our own write can keep the old stamp, so a recent
	// file is compared by content as well.
	if fi.Size() == cm.stat.size && fi.ModTime().Equal(cm.stat.mod) && time.Since(fi.ModTime()) > 3*time.Second {
		return
	}
	cm.stat = fileStamp{fi.Size(), fi.ModTime()}
	text, err := os.ReadFile(cm.path)
	if err != nil || shaOf(text) == cm.cur.SHA {
		return
	}
	f, cfg, diags := config.Load(cm.path, text, cm.app.validators())
	switch {
	case config.HasErrors(diags):
		cm.setBanner(&Banner{Kind: "error", Code: "invalid", Message: "monitors.conf has errors; the previous version keeps running", Diags: diags})
	case cfg.Serial == cm.cur.Serial:
		cm.setBanner(&Banner{Kind: "warning", Code: "serial_not_bumped", Message: "monitors.conf changed but the revision was not increased", Serial: cfg.Serial})
	case config.CheckSerial(cfg.Serial, cm.cur.Serial) != nil:
		cm.setBanner(&Banner{Kind: "warning", Code: "serial_lower", Message: config.CheckSerial(cfg.Serial, cm.cur.Serial).Error(), Serial: cfg.Serial})
	default:
		_ = cm.apply(text, f, cfg, "file", "", "", nil, nil)
	}
}

func (cm *configManager) setBanner(b *Banner) {
	cm.banner = b
	cm.app.emit("banner", b)
}

// bumpAndApply raises the serial of the file on disk and applies it (the
// "serial not increased" banner's button).
func (cm *configManager) bumpAndApply(author string, client map[string]any) (ConfigState, error) {
	text, err := os.ReadFile(cm.path)
	if err != nil {
		return cm.current(), err
	}
	return cm.save(text, "", author, "serial bumped from the UI", client)
}

// rollback re-applies an older version as a new serial.
func (cm *configManager) rollback(serial uint32, author string, client map[string]any) (ConfigState, error) {
	sn, err := cm.app.store.SnapshotText(serial)
	if err != nil {
		return cm.current(), fmt.Errorf("version %d not found", serial)
	}
	return cm.save([]byte(sn.Text), "", author, "rollback to "+strconv.FormatUint(uint64(serial), 10), client)
}

// writeAtomic replaces the file through a temporary file in the same
// directory, keeping its mode and writing through a symlink to its target.
func writeAtomic(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o640)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".monitors-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// audit records what a configuration change did, by record and section,
// whoever made it: the web, the API, or an edit of the file.
func (cm *configManager) audit(oldState, newState ConfigState, changes []config.Change, source, author, message string, client map[string]any) {
	if len(changes) == 0 {
		return
	}
	old, cfg := oldState.Config, newState.Config
	areaOf := func(name string) string {
		if m := cfg.FindMonitor(name); m != nil {
			return m.Area
		}
		if m := old.FindMonitor(name); m != nil {
			return m.Area
		}
		return ""
	}
	detail := map[string]any{"serial": cfg.Serial, "source": source}
	if message != "" {
		detail["message"] = message
	}
	areas := map[string]bool{}
	byKind := map[string][]string{}
	for _, c := range changes {
		if len(byKind[c.Kind]) < 50 {
			byKind[c.Kind] = append(byKind[c.Kind], strings.ToLower(c.Type)+" "+c.Record)
		}
		if c.Type == "MONITOR" {
			areas[areaOf(c.Record)] = true
		} else {
			areas[""] = true
		}
	}
	for k, v := range byKind {
		detail[k] = v
	}
	// Each record as it was and as it is now, so the log alone answers
	// "who changed what from what to what".
	var diff []map[string]string
	for _, c := range changes {
		if len(diff) == 50 {
			break
		}
		d := map[string]string{"record": c.Record, "type": strings.ToLower(c.Type), "kind": c.Kind}
		if b := recordText(oldState, c.Record); b != "" {
			d["before"] = b
		}
		if a := recordText(newState, c.Record); a != "" {
			d["after"] = a
		}
		diff = append(diff, d)
	}
	detail["diff"] = diff
	area := ""
	if len(areas) == 1 {
		for a := range areas {
			area = a
		}
	}
	if author == "" {
		author = source // an edit of the file on disk
	}
	if client != nil {
		detail["client"] = client
	}
	if err := cm.app.store.Audit(store.AuditEntry{Actor: author, Action: "config.apply", Target: "monitors.conf", Area: area, Detail: detail}); err != nil {
		cm.app.log.Error("audit", "err", err)
	}
}

// recordText is a record as written in a configuration, at most 2000
// characters.
func recordText(st ConfigState, name string) string {
	if st.File == nil {
		return ""
	}
	r, ok := st.File.Record(name)
	if !ok || r.Stmt == nil {
		return ""
	}
	lines := strings.Split(st.Text, "\n")
	from, to := r.Stmt.StartLine-1, r.Stmt.EndLine
	if from < 0 || to > len(lines) || from >= to {
		return ""
	}
	t := strings.TrimSpace(strings.Join(lines[from:to], "\n"))
	if len(t) > 2000 {
		t = t[:2000] + "…"
	}
	return t
}
