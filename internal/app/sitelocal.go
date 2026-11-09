package app

import (
	"errors"
	"os"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// The site-local zone: monitors, channels and policies that belong to this
// site only. It lives in its own file, is never sent to other nodes, never
// part of the master's state copy, and neither the master's sync, a join
// nor a promotion overwrites it. This node checks and alerts on it in every
// role: master, following replica, replica on its own, replaced master.

const (
	siteLocalFile       = "site-local.conf"
	legacySiteLocalFile = "site-local.bind" // before 2.6
)

// siteLocalPath is the zone's file.
func (a *App) siteLocalPath() string { return a.dataPath(siteLocalFile) }

// SiteLocal returns the zone's text ("" when there is none).
func (a *App) SiteLocal() string {
	b, _ := os.ReadFile(a.siteLocalPath())
	return string(b)
}

// loadSiteLocal parses the zone; nil when there is none or it has errors
// (they are logged, the rest of the configuration still applies).
func (a *App) loadSiteLocal() *config.Config {
	b, err := os.ReadFile(a.siteLocalPath())
	if err != nil || len(b) == 0 {
		return nil
	}
	_, cfg, diags := config.Load(a.siteLocalPath(), b, a.validators())
	if config.HasErrors(diags) {
		a.log.Warn("the site-local configuration has errors; it is not applied", "file", a.siteLocalPath())
		return nil
	}
	return cfg
}

// SetSiteLocal validates and saves the zone ("" removes it), then applies.
func (a *App) SetSiteLocal(text, author string) ([]config.Diagnostic, error) {
	if text == "" {
		if err := os.Remove(a.siteLocalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else {
		_, _, diags := config.Load(a.siteLocalPath(), []byte(text), a.validators())
		if config.HasErrors(diags) {
			return diags, errors.New("the site-local configuration has errors")
		}
		if err := os.WriteFile(a.siteLocalPath()+".tmp", []byte(text), 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(a.siteLocalPath()+".tmp", a.siteLocalPath()); err != nil {
			return nil, err
		}
	}
	_ = a.store.Audit(store.AuditEntry{Actor: author, Action: "config.site_local", Target: "site-local"})
	a.Reapply()
	return nil, nil
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
