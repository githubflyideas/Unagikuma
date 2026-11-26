package app

import (
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// 3.0 drops teams. Alerts go to the installation's alert channels (the
// "default" policy) unless a monitor names its own, and an account sees the
// monitor groups it is given. This one-time step rewrites a file with TEAM
// records so that nothing anyone sees or receives changes:
//
//   - the admin team's channels become the default policy's;
//   - another team's channels, when they differ, become a policy of the
//     team's name, named by policy= on the team's monitors;
//   - a team's monitors without group= get group=<team>;
//   - a member of teams (and not of the admin team) gets the groups of
//     those teams' monitors; everyone else keeps seeing everything;
//   - team= options and TEAM records are removed, and selectors naming a
//     team (team:NAME) name its group instead.
//
// The file before the change is kept as monitors.conf.before-groups, and
// the change is in the audit log.
func (a *App) migrateTeamsToGroups(path string) error {
	text, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	f, c, d := config.Load(path, text, a.validators())
	if c == nil || config.HasErrors(d) {
		return nil
	}
	explicit := map[string]*config.Team{}
	for _, t := range c.Teams {
		explicit[t.Name] = t
	}
	used := false
	for _, m := range c.Monitors {
		used = used || m.Team != ""
	}
	if len(explicit) == 0 && !used {
		return nil
	}
	admin := c.FindTeam(config.AdminTeam)
	sameAsAdmin := func(t *config.Team) bool {
		return slices.Equal(t.Channels, admin.Channels) && (t.Mode == "all") == (admin.Mode == "all")
	}
	policyOpts := func(t *config.Team) []config.Opt {
		opts := []config.Opt{{Key: "chain", Value: strings.Join(t.Channels, ",")}}
		if t.Mode == "all" {
			opts = append(opts, config.Opt{Key: "mode", Value: "all"})
		}
		return opts
	}
	// The records go first: a team's name becomes free for its policy.
	for name := range explicit {
		if err := f.DeleteRecord(name); err != nil {
			return err
		}
	}
	if len(admin.Channels) > 0 {
		if p := c.FindPolicy("default"); p == nil || p.Implicit || len(p.Chain) == 0 {
			if p != nil && !p.Implicit {
				if err := f.SetOpts("default", map[string]string{"chain": strings.Join(admin.Channels, ","), "mode": map[bool]string{true: "all"}[admin.Mode == "all"]}); err != nil {
					return err
				}
			} else if err := f.UpsertRecord(config.RecordSpec{Name: "default", Type: "POLICY", Opts: policyOpts(admin)}); err != nil {
				return err
			}
		}
	}
	teamPolicy := map[string]string{}
	for name, t := range explicit {
		if name == config.AdminTeam || len(t.Channels) == 0 || sameAsAdmin(t) || c.FindPolicy(name) != nil {
			continue
		}
		if err := f.UpsertRecord(config.RecordSpec{Name: name, Type: "POLICY", Opts: policyOpts(t)}); err != nil {
			return err
		}
		teamPolicy[name] = name
	}
	groupsOfTeam := map[string]map[string]bool{}
	for _, m := range c.Monitors {
		team := m.TeamOf()
		set := map[string]string{}
		if m.Team != "" {
			set["team"] = ""
		}
		group := m.Group
		if team != config.AdminTeam {
			if group == "" {
				group = team
				set["group"] = team
			}
			if _, own := m.Opts["policy"]; !own && !m.Mute && !m.SystemChannel {
				if teamPolicy[team] != "" {
					set["policy"] = teamPolicy[team]
				} else if t := c.FindTeam(team); t != nil && len(t.Channels) == 0 && t.Policy != "" {
					set["policy"] = t.Policy // a team of an older file, still on a policy
				}
			}
		}
		if groupsOfTeam[team] == nil {
			groupsOfTeam[team] = map[string]bool{}
		}
		groupsOfTeam[team][group] = true
		if len(set) > 0 {
			if err := f.SetOpts(m.Name, set); err != nil {
				return err
			}
		}
	}
	// Selectors (status pages, maintenance) named teams: now groups.
	for _, r := range f.Records() {
		for _, o := range r.Opts {
			if o.Key == "select" && strings.Contains(o.Value, "team:") && !strings.Contains(o.Value, "team:"+config.AdminTeam) {
				v := strings.ReplaceAll(o.Value, "team:", "group:")
				if err := f.SetOpts(r.Name, map[string]string{"select": v}); err != nil {
					return err
				}
			}
		}
	}
	serial, _, err := config.NextSerial(c.Serial, a.now())
	if err != nil {
		return err
	}
	if err := f.SetSerial(serial); err != nil {
		return err
	}
	if _, _, d := config.Load(path, f.Bytes(), a.validators()); config.HasErrors(d) {
		a.log.Warn("teams were not moved to groups: the result would not load", "file", path, "diagnostics", d)
		return nil
	}
	// Who sees what: a member of the admin team, or of no team, sees all.
	userGroups := map[string]map[string]bool{}
	seesAll := map[string]bool{}
	for _, t := range c.AllTeams() {
		for _, u := range t.Members {
			if t.Name == config.AdminTeam {
				seesAll[u] = true
				continue
			}
			if userGroups[u] == nil {
				userGroups[u] = map[string]bool{}
			}
			for g := range groupsOfTeam[t.Name] {
				userGroups[u][g] = true
			}
			if len(groupsOfTeam[t.Name]) == 0 {
				userGroups[u][t.Name] = true // a team without monitors yet
			}
		}
	}
	backup, err := os.OpenFile(path+".before-groups", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = backup.Write(text)
		backup.Close()
	}
	if err != nil && !os.IsExist(err) {
		return err
	}
	if err := writePeerAtomic(path, f.Bytes()); err != nil {
		return err
	}
	for u, gs := range userGroups {
		if seesAll[u] {
			continue
		}
		id, err := a.store.UserID(u)
		if err != nil {
			continue
		}
		var list []string
		for g := range gs {
			if g != "" {
				list = append(list, g)
			}
		}
		sort.Strings(list)
		if err := a.store.SetGroups(id, list); err != nil {
			return err
		}
	}
	var names []string
	for n := range explicit {
		names = append(names, n)
	}
	sort.Strings(names)
	return a.store.Audit(auditEntry("config.teams_to_groups", "system", map[string]any{"file": path, "teams": strings.Join(names, ",")}))
}
