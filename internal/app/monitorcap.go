package app

import (
	"fmt"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/license"
)

// The monitor limits. A single installation may have up to
// license.FreeSingle monitors in all: the shared configuration and this
// site's own zone together. In a cluster the shared configuration may
// have up to license.FreeShared monitors, and each site's own zone up to
// license.FreeSite, judged at that site alone. The two gateway monitors
// the installation seeds for itself are not the user's and never count;
// a paused monitor counts — it still holds a place. Nothing is ever
// switched off by a limit: growth past it is refused, the status quo
// always stands.

// CountMonitors counts cfg's monitors. seeded reports whether a record is
// one of the seeded gateway monitors.
func CountMonitors(cfg *config.Config, seeded func(name string) bool) int {
	if cfg == nil {
		return 0
	}
	n := 0
	for _, m := range cfg.Monitors {
		if seeded != nil && seeded(m.Name) {
			continue
		}
		n++
	}
	return n
}

// IsCluster reports whether cfg describes a cluster: it has a node other
// than this one.
func IsCluster(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, n := range cfg.Nodes {
		if n.Name != cfg.MName {
			return true
		}
	}
	return false
}

func (a *App) seeded(name string) bool { return GatewaySeeded(a.store.Setting, name) }

// MonitorCount is the count the shared configuration cfg is judged by: in
// a single installation with this site's own zone, in a cluster without.
func (a *App) MonitorCount(cfg *config.Config) int {
	n := CountMonitors(cfg, a.seeded)
	if !IsCluster(cfg) {
		n += a.localMonitorCount(cfg, a.appliedSiteLocal())
	}
	return n
}

// localMonitorCount counts the monitors of a site-local zone that run
// next to cfg: a name the shared configuration already uses runs as the
// shared one and is not counted twice.
func (a *App) localMonitorCount(cfg *config.Config, local *config.Config) int {
	if local == nil {
		return 0
	}
	n := 0
	for _, m := range local.Monitors {
		if cfg != nil && cfg.FindMonitor(m.Name) != nil {
			continue
		}
		n++
	}
	return n
}

// GatewaySeeded reports whether the store markers identify name as
// one of the gateway monitors the installation seeded for itself.
// setting is store.Setting; the import command counts with it too.
func GatewaySeeded(setting func(string) (string, error), name string) bool {
	for _, g := range gatewayMonitors {
		if g.name == name {
			if v, _ := setting(g.setting); v != "" {
				return true
			}
		}
	}
	return false
}

// MonitorLimit is the number of monitors a single installation may have
// (0 = no limit). A licence never gives fewer than the free number.
func MonitorLimit(st license.State) int { return limitOf(st, license.FreeSingle) }

// SharedLimit is the number of monitors a cluster's shared configuration
// may have, and SiteLimit the number each site's own zone may have.
func SharedLimit(st license.State) int { return limitOf(st, license.FreeShared) }
func SiteLimit(st license.State) int   { return limitOf(st, license.FreeSite) }

func limitOf(st license.State, free int) int {
	if !st.Registered {
		return free
	}
	if st.MaxMonitors > 0 && st.MaxMonitors < free {
		return free
	}
	return st.MaxMonitors
}

// LimitFor is the limit the shared configuration cfg is judged by.
func LimitFor(st license.State, cfg *config.Config) int {
	if IsCluster(cfg) {
		return SharedLimit(st)
	}
	return MonitorLimit(st)
}

// MonitorCap is the installation's standing against the monitor
// limit, for the registration page and the over-limit notice.
type MonitorCap struct {
	Used       int  `json:"used"`
	Limit      int  `json:"limit"` // 0 = no limit
	Over       bool `json:"over"`
	Registered bool `json:"registered"`
	// In a cluster: this site's own monitors and their limit.
	SiteUsed  int `json:"site_used,omitempty"`
	SiteLimit int `json:"site_limit,omitempty"`
}

// MonitorCap reports the current standing: in a cluster, of the shared
// configuration, with this site's own zone apart.
func (a *App) MonitorCap() MonitorCap {
	st := a.License().State()
	cfg := a.EffectiveConfig()
	limit := LimitFor(st, cfg)
	used := a.MonitorCount(cfg)
	c := MonitorCap{Used: used, Limit: limit, Over: limit > 0 && used > limit, Registered: st.Registered}
	if IsCluster(cfg) {
		c.SiteUsed, c.SiteLimit = a.localMonitorCount(cfg, a.appliedSiteLocal()), SiteLimit(st)
	}
	return c
}

// monitorCapDiag is the refusal a configuration change gets when it
// would raise the monitor count past the installation's limit and
// past what is already running: only growth beyond the limit is
// refused, never the status quo, and never a change that holds or
// lowers the count. Both are counted the way new is judged.
func (a *App) monitorCapDiag(old, new *config.Config) *config.Diagnostic {
	limit := LimitFor(a.License().State(), new)
	if limit == 0 {
		return nil
	}
	nn := a.MonitorCount(new)
	was := CountMonitors(old, a.seeded)
	if !IsCluster(new) {
		was += a.localMonitorCount(old, a.appliedSiteLocal())
	}
	if nn <= limit || nn <= was {
		return nil
	}
	what := "monitors"
	if IsCluster(new) {
		what = "shared monitors"
	}
	return &config.Diagnostic{Pos: config.Pos{Line: 1, Col: 1}, Severity: config.SevError, Code: "monitor_limit",
		Msg: fmt.Sprintf("this change would raise the number of %s to %d, past this installation's limit of %d; to register, please contact the author (Settings › Registration)", what, nn, limit)}
}

// ImportSHAKey is the store setting the import command uses to mark
// the configuration file it just wrote, so the running server applies
// it under the import ceiling instead of refusing it as a hand edit
// past the limit.
const ImportSHAKey = "import.expect_sha"

// consumeImportSHA reports whether sha is the file an import just
// wrote, and forgets the mark either way it matched.
func (a *App) consumeImportSHA(sha string) bool {
	v, _ := a.store.Setting(ImportSHAKey)
	if v == "" || v != sha {
		return false
	}
	_ = a.store.DeleteSetting(ImportSHAKey)
	return true
}

// forgetImportSHA drops a stale import mark (at startup, the file it
// named has long been applied or replaced).
func (a *App) forgetImportSHA() {
	_ = a.store.DeleteSetting(ImportSHAKey)
}

// LicenseChanged applies the configuration again: this site's own zone,
// held back by the limit, may now run.
func (a *App) LicenseChanged() {
	a.Reapply()
}
