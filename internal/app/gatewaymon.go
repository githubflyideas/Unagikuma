package app

import (
	"context"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/netscope"
)

// The two gateways are where most outages of a single machine start: the
// router in front of it (the LAN gateway, found from the routing table) and
// the address on the internet chosen as the internet gateway. Each gets a
// ping monitor of its own, added once the user has monitors of their own. A monitor the user deletes stays
// deleted; one whose gateway moves follows it.

// gatewayMonitor is one of the monitors added for a gateway.
type gatewayMonitor struct {
	name    string // record name
	setting string // remembers the address it was added for
	title   string // i18n key of its title
	addr    func(a *App) string
}

var gatewayMonitors = []gatewayMonitor{
	{name: "lan-gateway", setting: "auto_monitor_lan_gateway", title: "sys.auto_lan_gateway", addr: func(*App) string {
		gws, _ := netscope.DefaultGateways()
		for _, g := range gws {
			if g.Addr.Is4() {
				return g.Addr.String()
			}
		}
		if len(gws) > 0 {
			return gws[0].Addr.String()
		}
		return ""
	}},
	{name: "internet-gateway", setting: "auto_monitor_internet_gateway", title: "sys.auto_internet_gateway", addr: func(a *App) string {
		return a.cfg.current().Config.Tunables.Raw("localnet.exit-target")
	}},
}

// hasOwnMonitors reports whether the configuration has a monitor other
// than the gateway ones.
func hasOwnMonitors(c *config.Config) bool {
	for _, m := range c.Monitors {
		own := true
		for _, g := range gatewayMonitors {
			if m.Name == g.name {
				own = false
			}
		}
		if own {
			return true
		}
	}
	return false
}

// runGatewayMonitors checks the gateways now and every minute after.
func (a *App) runGatewayMonitors(ctx context.Context) {
	if !a.opts.GatewayMonitors {
		return
	}
	a.ensureGatewayMonitors()
	a.every(ctx, time.Minute, a.ensureGatewayMonitors)
}

func (a *App) ensureGatewayMonitors() {
	if a.editable() != nil {
		return // a node of a cluster runs its master's configuration
	}
	cur := a.Config()
	if !cur.Config.Tunables.Bool("localnet.gateway-monitors") || !hasOwnMonitors(cur.Config) {
		return // a new installation starts with the first-run guide, not with these
	}
	for _, g := range gatewayMonitors {
		addr := g.addr(a)
		if addr == "" {
			continue
		}
		before, _ := a.store.Setting(g.setting)
		if before == addr {
			continue
		}
		_, exists := a.Config().File.Record(g.name)
		if before != "" && !exists {
			continue // deleted by the user
		}
		if before == "" && exists {
			// A record of that name the user wrote: leave it to them.
			_ = a.store.SetSetting(g.setting, addr)
			continue
		}
		spec := config.RecordSpec{Name: g.name, Interval: "1m", Type: "PING", Target: addr,
			Opts: []config.Opt{{Key: "attempts", Value: "1"}, {Key: "title", Value: i18n.T(a.messageLang(), g.title)}}}
		change := func(f *config.File) error {
			if r, ok := f.Record(g.name); ok {
				// The gateway moved: only the address changes, the user's
				// own settings stay.
				spec = config.RecordSpec{Name: r.Name, Interval: r.Interval, Type: r.Type, Target: addr, Opts: r.Opts}
			}
			return f.UpsertRecord(spec)
		}
		if _, err := a.EditConfig("system", "gateway monitor", change); err != nil {
			a.log.Warn("could not add the gateway monitor", "monitor", g.name, "err", err)
			continue
		}
		_ = a.store.SetSetting(g.setting, addr)
	}
}
