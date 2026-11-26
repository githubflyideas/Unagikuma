package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// The site-local zone: monitors, channels and policies that belong to this
// site only. It lives in its own file, is never sent to other nodes, never
// part of the master's state copy, and neither the master's sync, a join
// nor a promotion overwrites it. This node checks and alerts on it in every
// role: master, following replica, replica on its own, replaced master.

// SiteLocalFile is the zone's file name in the data directory. Its name
// matches monitors.conf so the two are recognised as the same format.
const SiteLocalFile = "local.monitors.conf"

// Earlier names of the zone's file, renamed at startup.
var legacySiteLocalFiles = []string{
	"site-local.bind", // before 2.6
	"site-local.conf", // 2.6 to 2.6.8
}

// siteLocalPath is the zone's file.
func (a *App) siteLocalPath() string { return a.dataPath(SiteLocalFile) }

// SiteLocal returns the zone's text ("" when there is none).
func (a *App) SiteLocal() string {
	b, _ := os.ReadFile(a.siteLocalPath())
	return string(b)
}

// siteLocalAppliedKey keeps the text of the zone last applied: a zone
// that grows past the monitor limit is not applied, the last one runs on.
const siteLocalAppliedKey = "site_local.applied"

// appliedSiteLocal is the zone as last applied (nil when there is none).
func (a *App) appliedSiteLocal() *config.Config {
	v, _ := a.store.Setting(siteLocalAppliedKey)
	if v == "" {
		return nil
	}
	_, cfg, diags := config.Load(a.siteLocalPath(), []byte(v), a.validators())
	if config.HasErrors(diags) {
		return nil
	}
	return cfg
}

// siteLocalCapDiag refuses a zone that would take the installation past
// its monitor limit and past what already runs: like any other change,
// only growth beyond the limit is refused.
func (a *App) siteLocalCapDiag(next *config.Config) *config.Diagnostic {
	limit := MonitorLimit(a.License().State())
	global := a.Config().Config
	shared := CountMasterMonitors(global, func(name string) bool { return GatewaySeeded(a.store.Setting, name) }) + a.otherSitesLocal(global)
	if u := a.clusterUsage.Load(); u != nil {
		// A node: the licence is the master's, and its standing covers the
		// shared monitors and every site's own; this site's part is
		// replaced by the new zone. Without word from the master yet, the
		// node judges by its own licence and count.
		limit = u.Limit
		global = a.EffectiveConfig()
		shared = u.Used - a.localMonitorCount(global, a.appliedSiteLocal())
	}
	if limit == 0 {
		return nil
	}
	was := shared + a.localMonitorCount(global, a.appliedSiteLocal())
	now := shared + a.localMonitorCount(global, next)
	if now <= limit || now <= was {
		return nil
	}
	return &config.Diagnostic{Pos: config.Pos{Line: 1, Col: 1}, Severity: config.SevError, Code: "monitor_limit",
		Msg: fmt.Sprintf("this site's own monitors would raise the number of monitors to %d, past this installation's limit of %d; to register, please contact the author (Settings › Registration)", now, limit)}
}

// loadSiteLocal parses the zone; nil when there is none or it has errors
// (they are logged, the rest of the configuration still applies). A zone
// changed by hand past the monitor limit is not applied: the last applied
// one runs on, and a banner says why.
func (a *App) loadSiteLocal() *config.Config {
	b, err := os.ReadFile(a.siteLocalPath())
	if err != nil || len(b) == 0 {
		_ = a.store.DeleteSetting(siteLocalAppliedKey)
		return nil
	}
	_, cfg, diags := config.Load(a.siteLocalPath(), b, a.validators())
	if config.HasErrors(diags) {
		a.log.Warn("the site-local configuration has errors; it is not applied", "file", a.siteLocalPath())
		return nil
	}
	applied, _ := a.store.Setting(siteLocalAppliedKey)
	if applied == string(b) {
		return cfg
	}
	if applied != "" { // the first zone seen (an upgrade) is the status quo
		if d := a.siteLocalCapDiag(cfg); d != nil {
			a.log.Warn("the site-local configuration grew past the monitor limit; the last applied one runs on", "file", a.siteLocalPath())
			a.cfg.setBanner(&Banner{Kind: "error", Code: "monitor_limit", Message: d.Msg, Diags: []config.Diagnostic{*d}})
			return a.appliedSiteLocal()
		}
	}
	_ = a.store.SetSetting(siteLocalAppliedKey, string(b))
	return cfg
}

// withFileHeader puts the $FILE line in front of a zone that has none: the
// zone is only edited here, so its revision needs no care, and asking a
// person to type a header first only gets in the way.
func withFileHeader(text string) string {
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, ";") {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(l), "$FILE") {
			return text
		}
		break
	}
	return "$FILE revision=1\n" + text
}

// SetSiteLocal validates and saves the zone ("" removes it), then applies.
// It returns the text as saved: a missing $FILE line is added.
func (a *App) SetSiteLocal(text, author string) (string, []config.Diagnostic, error) {
	return a.setSiteLocal(text, author, false)
}

// setSiteLocal is SetSiteLocal; imported skips the everyday monitor limit,
// which an import's ceiling has already held.
func (a *App) setSiteLocal(text, author string, imported bool) (string, []config.Diagnostic, error) {
	if strings.TrimSpace(text) == "" {
		text = ""
	} else {
		text = withFileHeader(text)
	}
	if text == "" {
		if err := os.Remove(a.siteLocalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return text, nil, err
		}
	} else {
		_, next, diags := config.Load(a.siteLocalPath(), []byte(text), a.validators())
		if config.HasErrors(diags) {
			return text, diags, errors.New("the site-local configuration has errors")
		}
		if d := a.siteLocalCapDiag(next); d != nil && !imported {
			return text, []config.Diagnostic{*d}, errors.New(d.Msg)
		}
		if err := os.WriteFile(a.siteLocalPath()+".tmp", []byte(text), 0o600); err != nil {
			return text, nil, err
		}
		if err := os.Rename(a.siteLocalPath()+".tmp", a.siteLocalPath()); err != nil {
			return text, nil, err
		}
		// Checked above: it is what runs from now on.
		_ = a.store.SetSetting(siteLocalAppliedKey, text)
	}
	_ = a.store.Audit(store.AuditEntry{Actor: author, Action: "config.site_local", Target: "site-local"})
	a.Reapply()
	return text, nil, nil
}

// addSiteLocal adds the zone to what this node's engine runs. Names the
// cluster configuration already uses keep the cluster's definition.
func (a *App) addSiteLocal(ec *engine.Config, lc *config.Config) {
	if lc == nil {
		return
	}
	le := a.engineConfig(lc)
	taken := map[string]bool{}
	for _, m := range ec.Monitors {
		taken[m.Name] = true
	}
	mons := append([]engine.MonitorSpec(nil), ec.Monitors...)
	for _, m := range le.Monitors {
		if taken[m.Name] {
			a.log.Warn("a site-local monitor has the name of a cluster monitor; the cluster's is used", "monitor", m.Name)
			continue
		}
		m.Nodes = nil
		mons = append(mons, m)
	}
	ec.Monitors = mons
	ch := make(map[string]engine.ChannelSpec, len(ec.Channels)+len(le.Channels))
	for k, v := range ec.Channels {
		ch[k] = v
	}
	for k, v := range le.Channels {
		if _, ok := ch[k]; !ok {
			ch[k] = v
		}
	}
	ec.Channels = ch
	pol := make(map[string]engine.PolicySpec, len(ec.Policies)+len(le.Policies))
	for k, v := range ec.Policies {
		pol[k] = v
	}
	for k, v := range le.Policies {
		// The site's own policy wins over a built-in one without channels.
		if cur, ok := pol[k]; !ok || len(cur.Chain) == 0 {
			pol[k] = v
		}
	}
	ec.Policies = pol
}

// siteLocalDefs are the zone's monitors, for history ids.
func siteLocalDefs(lc *config.Config) []store.MonitorDef {
	if lc == nil {
		return nil
	}
	var out []store.MonitorDef
	for _, m := range lc.Monitors {
		out = append(out, store.MonitorDef{Name: m.Name, DefHash: m.DefHash})
	}
	return out
}

// ---- a site's own configuration in a cluster ----
//
// A node reports its own configuration to the master with its status: how
// many monitors it runs (counted toward the cluster's monitor limit) and,
// when it changed, its text — kept on the master as a copy, never applied,
// never sent on, so that a site's own monitors survive the loss of its disk.
// The master sends every node the cluster's standing against the limit, by
// which a node judges additions to its own configuration; without word from
// the master a node keeps to the last standing it had.

const siteLocalCountsKey = "site_local.counts" // master: node -> monitors

// localReport is this site's own configuration for the master.
func (a *App) localReport() (int, string, string) {
	text, _ := a.store.Setting(siteLocalAppliedKey)
	return a.localMonitorCount(a.EffectiveConfig(), a.appliedSiteLocal()), shaOf([]byte(text)), text
}

// siteLocalCounts are the other sites' own monitors, as they last said.
func (a *App) siteLocalCounts() map[string]int {
	out := map[string]int{}
	if v, _ := a.store.Setting(siteLocalCountsKey); v != "" {
		_ = json.Unmarshal([]byte(v), &out)
	}
	return out
}

// otherSitesLocal is the sum of the other sites' own monitors, for the
// nodes cfg still has.
func (a *App) otherSitesLocal(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	n := 0
	for node, c := range a.siteLocalCounts() {
		if node != cfg.MName && cfg.FindNode(node) != nil {
			n += c
		}
	}
	return n
}

// onSiteLocalReport keeps what a node says about its own configuration.
func (a *App) onSiteLocalReport(node string, count int, hash string, text *string) {
	if text != nil {
		dir := a.dataPath("sites")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			path := filepath.Join(dir, safeFileName(node)+"."+SiteLocalFile)
			if err := os.WriteFile(path+".tmp", []byte(*text), 0o600); err == nil {
				_ = os.Rename(path+".tmp", path)
			}
		}
	}
	a.siteLocalMu.Lock()
	counts := a.siteLocalCounts()
	changed := counts[node] != count
	if changed {
		counts[node] = count
		b, _ := json.Marshal(counts)
		_ = a.store.SetSetting(siteLocalCountsKey, string(b))
	}
	a.siteLocalMu.Unlock()
	if changed {
		go a.Reapply() // the nodes get the new standing
	}
}

// safeFileName keeps letters, digits, '-', '_' and '.' of a node's name.
func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// setClusterUsage keeps the master's word on the cluster's standing. Only
// a node is told it, so having it means this server is a node; it is
// dropped when the server leaves the cluster or becomes its master.
func (a *App) setClusterUsage(u cluster.Usage) {
	a.clusterUsage.Store(&u)
}
