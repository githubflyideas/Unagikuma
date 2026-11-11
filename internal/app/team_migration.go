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
	m := &policyMigration{f: f, c: c, written: map[string]*config.Policy{}, teamPolicy: map[string]string{}}
	for _, p := range c.Policies {
		if !p.Implicit && p.Team == "" && !p.Mute {
			m.written[p.Name] = p
		}
	}
	if len(m.written) == 0 {
		return nil
	}
	for _, step := range []func() error{m.teams, m.systemChannels, m.monitors} {
		if err := step(); err != nil {
			return err
		}
	}
	if !m.changed {
		return nil
	}
	m.dropUnused(path, a.validators())
	return a.writeMigration(path, text, m)
}

// policyMigration turns policies that only list channels into team
// channels.
type policyMigration struct {
	f          *config.File
	c          *config.Config
	written    map[string]*config.Policy
	teamPolicy map[string]string
	changed    bool
}

// simplePolicy lists channels and nothing else (mode aside).
func simplePolicy(p *config.Policy) bool {
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

func (m *policyMigration) set(name string, opts map[string]string) error {
	m.changed = true
	return m.f.SetOpts(name, opts)
}

func (m *policyMigration) upsertTeam(t config.Team) error {
	m.changed = true
	return m.f.UpsertRecord(teamRecord(m.f, t))
}

// teams: their policy, or "default", becomes their channels.
func (m *policyMigration) teams() error {
	for _, t := range m.c.AllTeams() {
		pol := t.Policy
		if pol == "" {
			pol = "default"
		}
		m.teamPolicy[t.Name] = pol
		p := m.written[pol]
		if len(t.Channels) > 0 || !simplePolicy(p) {
			if t.Legacy { // managers=/operators=/viewers= become members=
				if err := m.upsertTeam(*t); err != nil {
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
		if err := m.upsertTeam(next); err != nil {
			return err
		}
	}
	return nil
}

// systemChannels: without any, the channels of the system policy (or of
// default) become them.
func (m *policyMigration) systemChannels() error {
	for _, ch := range m.c.Channels {
		if ch.System {
			return nil
		}
	}
	src := m.written["system"]
	if src == nil {
		src = m.written["default"]
	}
	if !simplePolicy(src) {
		return nil
	}
	for _, ch := range src.Chain {
		if cc := m.c.FindChannel(ch); cc != nil && cc.Kind != "group" {
			if err := m.set(ch, map[string]string{"system": "yes"}); err != nil {
				return err
			}
		}
	}
	return nil
}

// monitors drop policy= where their team now does the same.
func (m *policyMigration) monitors() error {
	for _, mon := range m.c.Monitors {
		v, own := mon.Opts["policy"]
		var err error
		switch {
		case !own:
		case v == "system":
			err = m.set(mon.Name, map[string]string{"policy": "", "system-channel": "yes"})
		case v == m.teamPolicy[mon.TeamOf()] && simplePolicy(m.written[v]):
			err = m.set(mon.Name, map[string]string{"policy": ""})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// dropUnused deletes the simple policies nothing refers to any more.
func (m *policyMigration) dropUnused(path string, o config.Options) {
	for name, p := range m.written {
		if !simplePolicy(p) {
			continue
		}
		before := m.f.Bytes()
		if err := m.f.DeleteRecord(name); err != nil {
			continue
		}
		if _, _, d := config.Load(path, m.f.Bytes(), o); config.HasErrors(d) {
			nf, _ := config.Parse(path, before)
			m.f = nf
		}
	}
}

// writeMigration raises the revision, keeps the old file next to the new
// one and audits the change.
func (a *App) writeMigration(path string, text []byte, m *policyMigration) error {
	f := m.f
	serial, _, err := config.NextSerial(m.c.Serial, a.now())
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
	for n := range m.written {
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
