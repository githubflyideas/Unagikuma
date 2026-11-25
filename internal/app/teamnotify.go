package app

import (
	"slices"
	"sort"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// When the shared infrastructure fails (a monitor of the built-in admin
// team), the administrators who are members of the teams it affects are
// told as well as the usual channels: the teams with monitors that depend
// on it, or every team when none does. Each is reached through the
// channels their contact entry names (Notify via), at their own address.
//
// This is built from what the engine already has: every team gets a
// contact group of its managers' personal channels, and a shared monitor
// gets a copy of its policy whose first escalation step, right away, goes
// to the groups of the teams it affects. So the notice is capped, logged
// and reported like any escalation, and is not sent once the problem is
// acknowledged.

// adminUsers is the set of enabled administrator accounts.
func (a *App) adminUsers() map[string]bool {
	out := map[string]bool{}
	users, _ := a.store.Users()
	for _, u := range users {
		if u.Role == store.RoleAdmin && !u.Disabled {
			out[u.Username] = true
		}
	}
	return out
}

// personalChannels makes copies of the channels a person chose under
// Notify via, each sending to their own addresses only; names start with
// prefix.
func personalChannels(ec *engine.Config, prefix string, p store.Person) []string {
	var out []string
	for _, via := range p.Notify {
		base, ok := ec.Channels[via]
		if !ok {
			continue
		}
		key, kind, ok := directory.AddressKey(base.Kind)
		if !ok {
			continue
		}
		addrs := p.Emails
		if kind == store.ContactPhone {
			addrs = p.Phones
		}
		if len(addrs) == 0 {
			continue
		}
		opts := make(map[string]string, len(base.Opts))
		for k, v := range base.Opts {
			opts[k] = v
		}
		opts[key] = strings.Join(addrs, ",")
		name := prefix + via
		cs := base
		cs.Channel = notify.Channel{Name: name, Kind: base.Kind, Opts: opts}
		cs.Members, cs.NoRecipients = nil, ""
		ec.Channels[name] = cs
		out = append(out, name)
	}
	return out
}

// addTeamFallbacks tells a team's administrators (the administrators in
// the team, or every administrator when it has none) when its channels
// fail, and when its monitors go to the system channels because the team
// has none.
func (a *App) addTeamFallbacks(cfg *config.Config, ec *engine.Config) {
	admins := a.adminUsers()
	people := map[string]store.Person{}
	for _, p := range a.Directory().People() {
		if p.Username != "" && p.Active() {
			people[p.Username] = p
		}
	}
	group := map[string]string{}
	groupOf := func(t *config.Team) string {
		if g, ok := group[t.Name]; ok {
			return g
		}
		var users []string
		for _, u := range t.Members {
			if admins[u] {
				users = append(users, u)
			}
		}
		if len(users) == 0 {
			for u := range admins {
				users = append(users, u)
			}
			sort.Strings(users)
		}
		var members []string
		for _, u := range users {
			if p, ok := people[u]; ok {
				members = append(members, personalChannels(ec, "admin:"+t.Name+":"+u+":", p)...)
			}
		}
		g := ""
		if len(members) > 0 {
			g = "admins:" + t.Name
			ec.Channels[g] = engine.ChannelSpec{Channel: notify.Channel{Name: g, Kind: "group"}, Members: members}
		}
		group[t.Name] = g
		return g
	}
	// The channels that reach each team's administrators directly (told
	// when a recipient is left out, contacts.notify-admins).
	adminChans := map[string][]notify.Channel{}
	for _, t := range cfg.AllTeams() {
		if g := groupOf(t); g != "" {
			for _, m := range ec.Channels[g].Members {
				adminChans[t.Name] = append(adminChans[t.Name], ec.Channels[m].Channel)
			}
		}
	}
	a.teamAdmins.Store(&adminChans)
	// When a team's channels fail: the system channels, and the team's
	// administrators.
	var system []string
	if sp := cfg.FindPolicy("system"); sp != nil {
		system = sp.Chain
	}
	for _, p := range cfg.Policies {
		if p.Team == "" {
			continue
		}
		t := cfg.FindTeam(p.Team)
		if t == nil {
			continue
		}
		var onFail [][]string
		if len(system) > 0 && !slices.Equal(system, p.Chain) {
			onFail = append(onFail, system)
		}
		if g := groupOf(t); g != "" {
			onFail = append(onFail, []string{g})
		}
		if len(onFail) > 0 {
			ps := ec.Policies[p.Name]
			ps.OnFail = onFail
			ec.Policies[p.Name] = ps
		}
	}
	for i := range ec.Monitors {
		ms := &ec.Monitors[i]
		m := cfg.FindMonitor(ms.Name)
		if m == nil || !m.Unrouted {
			continue
		}
		t := cfg.FindTeam(m.TeamOf())
		if t == nil {
			continue
		}
		g := groupOf(t)
		base, ok := ec.Policies[ms.Policy]
		if g == "" || !ok {
			continue
		}
		name := base.Name + " + admins of " + t.Name
		if _, done := ec.Policies[name]; !done {
			p := base
			p.Name = name
			p.Escalate = append([]engine.EscalationStep{{After: 0, Channels: []string{g}}}, base.Escalate...)
			ec.Policies[name] = p
		}
		ms.Policy = name
	}
}

// managerGroupName names the contact group of a team's administrators. Record
// names cannot contain ':', so it never clashes with a channel.
func managerGroupName(team string) string { return "managers:" + team }

// addManagerNotices adds the managers' channels and groups, and the
// policies of the shared monitors that affect teams, to ec.
func (a *App) addManagerNotices(cfg *config.Config, ec *engine.Config) {
	if len(cfg.Teams) == 0 {
		return
	}
	admins := a.adminUsers()
	people := map[string]store.Person{}
	for _, p := range a.Directory().People() {
		if p.Username != "" && p.Active() {
			people[p.Username] = p
		}
	}
	groups := map[string]bool{}
	for _, t := range cfg.Teams {
		var members []string
		for _, u := range t.Members {
			if !admins[u] {
				continue
			}
			if p, ok := people[u]; ok {
				members = append(members, personalChannels(ec, "manager:"+t.Name+":"+u+":", p)...)
			}
		}
		if len(members) > 0 {
			g := managerGroupName(t.Name)
			ec.Channels[g] = engine.ChannelSpec{Channel: notify.Channel{Name: g, Kind: "group"}, Members: members}
			groups[t.Name] = true
		}
	}
	if len(groups) == 0 {
		return
	}
	affected := affectedTeams(cfg)
	var all []string
	for t := range groups {
		all = append(all, t)
	}
	sort.Strings(all)
	for i := range ec.Monitors {
		ms := &ec.Monitors[i]
		m := cfg.FindMonitor(ms.Name)
		if m == nil || m.Team != "" || m.Mute {
			continue
		}
		teams := affected[m.Name]
		if len(teams) == 0 {
			teams = all
		}
		var chans []string
		for _, t := range teams {
			if groups[t] {
				chans = append(chans, managerGroupName(t))
			}
		}
		if len(chans) == 0 {
			continue
		}
		base, ok := ec.Policies[ms.Policy]
		if !ok {
			continue
		}
		name := base.Name + " + managers of " + strings.Join(teams, ", ")
		if _, done := ec.Policies[name]; !done {
			p := base
			p.Name = name
			p.Escalate = append([]engine.EscalationStep{{After: 0, Channels: chans}}, base.Escalate...)
			ec.Policies[name] = p
		}
		ms.Policy = name
	}
}

// affectedTeams maps each monitor to the teams whose monitors depend on it,
// directly or through other monitors.
func affectedTeams(cfg *config.Config) map[string][]string {
	sets := map[string]map[string]bool{}
	for _, m := range cfg.Monitors {
		if m.Team == "" {
			continue
		}
		seen := map[string]bool{}
		var up func(names []string)
		up = func(names []string) {
			for _, p := range names {
				if seen[p] {
					continue
				}
				seen[p] = true
				if sets[p] == nil {
					sets[p] = map[string]bool{}
				}
				sets[p][m.Team] = true
				if pm := cfg.FindMonitor(p); pm != nil {
					up(pm.Parents)
				}
			}
		}
		up(m.Parents)
	}
	out := map[string][]string{}
	for mon, set := range sets {
		for t := range set {
			out[mon] = append(out[mon], t)
		}
		sort.Strings(out[mon])
	}
	return out
}
