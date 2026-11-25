package app

import (
	"fmt"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/license"
)

// The monitor limit counts what runs on the master: the monitors every
// node checks (global) and the ones placed on the master itself (its
// region's, or named to it directly). The licence is judged there, so
// the count is too. The two gateway monitors the installation seeds
// for itself are not the user's and never count; a paused monitor
// counts — it still holds a place. Nothing is ever switched off by
// the limit: growth past it is refused, the status quo always stands.

// CountMasterMonitors counts cfg's monitors in the master's running
// set. seeded reports whether a record is one of the seeded gateway
// monitors.
func CountMasterMonitors(cfg *config.Config, seeded func(name string) bool) int {
	if cfg == nil {
		return 0
	}
	n := 0
	for _, m := range cfg.Monitors {
		if seeded != nil && seeded(m.Name) {
			continue
		}
		if len(m.Nodes) == 0 || containsStr(m.Nodes, cfg.MName) {
			n++
		}
	}
	return n
}

// MonitorCount is the installation's monitor count against the limit,
// for a configuration (the running one, or one about to be applied).
func (a *App) MonitorCount(cfg *config.Config) int {
	return CountMasterMonitors(cfg, func(name string) bool { return GatewaySeeded(a.store.Setting, name) })
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

// MonitorLimit is the number of monitors the installation may have
// (0 = no limit). A licence never gives fewer than the free number.
func MonitorLimit(st license.State) int {
	if !st.Registered {
		return license.FreeMonitors
	}
	if st.MaxMonitors > 0 && st.MaxMonitors < license.FreeMonitors {
		return license.FreeMonitors
	}
	return st.MaxMonitors
}

// AreaInMasterSet reports whether monitors placed in the $SCOPE
// section area land in the master's running set (and so count toward
// the monitor limit and the import ceiling).
func AreaInMasterSet(area string, cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	switch area {
	case "", "*":
		return true
	}
	if r, ok := strings.CutPrefix(area, "region:"); ok {
		n := cfg.FindNode(cfg.MName)
		return n != nil && n.Region == r
	}
	for _, name := range strings.Split(area, ",") {
		if strings.TrimSpace(name) == cfg.MName {
			return true
		}
	}
	return false
}

// MonitorCap is the installation's standing against the monitor
// limit, for the registration page and the over-limit notice.
type MonitorCap struct {
	Used       int  `json:"used"`
	Limit      int  `json:"limit"` // 0 = no limit
	Over       bool `json:"over"`
	Registered bool `json:"registered"`
}

// MonitorCap reports the current standing.
func (a *App) MonitorCap() MonitorCap {
	st := a.License().State()
	limit := MonitorLimit(st)
	used := a.MonitorCount(a.Config().Config)
	return MonitorCap{Used: used, Limit: limit, Over: limit > 0 && used > limit, Registered: st.Registered}
}

// monitorCapDiag is the refusal a configuration change gets when it
// would raise the monitor count past the installation's limit and
// past what is already running: only growth beyond the limit is
// refused, never the status quo, and never a change that holds or
// lowers the count.
func (a *App) monitorCapDiag(old, new *config.Config) *config.Diagnostic {
	limit := MonitorLimit(a.License().State())
	if limit == 0 {
		return nil
	}
	nn := a.MonitorCount(new)
	if nn <= limit || nn <= a.MonitorCount(old) {
		return nil
	}
	return &config.Diagnostic{Pos: config.Pos{Line: 1, Col: 1}, Severity: config.SevError, Code: "monitor_limit",
		Msg: fmt.Sprintf("this change would raise the number of monitors to %d, past this installation's limit of %d; to register, please contact the author (Settings › Registration)", nn, limit)}
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
