package app

import (
	"os"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// Before 2.6, notifications went through POLICY records: one per team
// (policy= on TEAM), "default" for the rest, and "system" for notices about
// the monitoring itself. 2.6 sends a team's notifications to the team's
// channels instead. This one-time step rewrites a file of the old kind:
//
//   - a team's simple policy (only chain= and mode=) becomes its channels;
//     a team without one, and the admin team, get the simple "default";
//   - the channels of a simple "system" policy (or, when there is none,
//     of "default") are marked system=yes;
//   - policy= on monitors that only repeated their team's is dropped, and
//     policy=system becomes system-channel=yes;
//   - simple policies nothing refers to any more are deleted;
//   - the role lists of teams (managers=, operators=, viewers=) become
//     members=.
//
// Policies with more (repeat=, escalate=, ...) are left as they are and
// keep working. The file before the change is kept as
// monitors.conf.before-teams, and the change is in the audit log.
func (a *App) migratePoliciesToTeams(path string) error {
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
	written := map[string]*config.Policy{}
	for _, p := range c.Policies {
		if !p.Implicit && p.Team == "" && !p.Mute {
			written[p.Name] = p
		}
	}
	if len(written) == 0 {
		return nil
	}
	simple := func(p *config.Policy) bool {
		if p == nil || len(p.Chain) == 0 {
			return false
		}
		for k := range p.Opts {
			if k != "chain" && k != "mode" {
				return false
			}
		}
		return true
	}
	changed := false
	set := func(name string, opts map[string]string) error {
		changed = true
		return f.SetOpts(name, opts)
	}
	def := written["default"]
	// Teams: their policy, or "default", becomes their channels.
	teamPolicy := map[string]string{}
	for _, t := range c.AllTeams() {
		pol := t.Policy
		if pol == "" {
			pol = "default"
		}
		teamPolicy[t.Name] = pol
		p := written[pol]
		if len(t.Channels) > 0 || !simple(p) {
			if t.Legacy { // managers=/operators=/viewers= become members=
				changed = true
				if err := f.UpsertRecord(teamRecord(f, *t)); err != nil {
					return err
				}
			}
			continue
		}
		next := *t
		next.Channels, next.Mode, next.Policy = p.Chain, p.Mode, ""
		if next.Mode == "fallback" {
			next.Mode = ""
		}
		changed = true
		if err := f.UpsertRecord(teamRecord(f, next)); err != nil {
			return err
		}
	}
	// System channels.
	hasSystem := false
	for _, ch := range c.Channels {
		hasSystem = hasSystem || ch.System
	}
	if sys := written["system"]; !hasSystem {
		src := sys
		if src == nil {
			src = def
		}
		if simple(src) {
			for _, ch := range src.Chain {
				if cc := c.FindChannel(ch); cc != nil && cc.Kind != "group" {
					if err := set(ch, map[string]string{"system": "yes"}); err != nil {
						return err
					}
				}
			}
		}
	}
	// Monitors.
	for _, m := range c.Monitors {
		v, own := m.Opts["policy"]
		if !own {
			continue
		}
		switch {
		case v == "system":
			if err := set(m.Name, map[string]string{"policy": "", "system-channel": "yes"}); err != nil {
				return err
			}
		case v == teamPolicy[m.TeamOf()] && simple(written[v]):
			if err := set(m.Name, map[string]string{"policy": ""}); err != nil {
				return err
			}
		}
	}
	if !changed {
		return nil
	}
	// Simple policies nothing refers to any more.
	for name, p := range written {
		if !simple(p) {
			continue
		}
		before := f.Bytes()
		if err := f.DeleteRecord(name); err != nil {
			continue
		}
		if _, _, d := config.Load(path, f.Bytes(), a.validators()); config.HasErrors(d) {
			nf, _ := config.Parse(path, before)
			f = nf
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
		a.log.Warn("policies were not moved to team channels: the result would not load", "file", path)
		return nil
	}
	backup, err := os.OpenFile(path+".before-teams", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
	var names []string
	for n := range written {
		names = append(names, n)
	}
	return a.store.Audit(auditEntry("config.team_channels_migration", "system", map[string]any{"file": path, "policies": strings.Join(names, ",")}))
}

// teamRecord renders a team, keeping options it does not set.
func teamRecord(f *config.File, t config.Team) config.RecordSpec {
	var opts []config.Opt
	add := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			opts = append(opts, config.Opt{Key: k, Value: v})
		}
	}
	if t.Title != t.Name {
		add("title", t.Title)
	}
	add("members", strings.Join(t.Members, ","))
	add("channels", strings.Join(t.Channels, ","))
	if t.Mode == "all" {
		add("mode", "all")
	}
	add("policy", t.Policy)
	add("description", t.Description)
	if rec, ok := f.Record(t.Name); ok {
		for _, o := range rec.Opts {
			switch o.Key {
			case "title", "members", "managers", "operators", "viewers", "channels", "mode", "policy", "description":
			default:
				opts = append(opts, o)
			}
		}
	}
	return config.RecordSpec{Name: t.Name, Type: "TEAM", Opts: opts}
}
