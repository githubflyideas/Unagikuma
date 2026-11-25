package app

import (
	"fmt"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// SetPaused writes disabled=yes on the monitors' records, or removes it.
// Used when editing the shared configuration file itself. The pause and
// resume API uses site-local overlay (see SetLocalPaused) instead, so a
// pause on one site does not change monitors.conf or other sites.
func SetPaused(f *config.File, names []string, paused bool) error {
	for _, name := range names {
		var rec config.RecordSpec
		found := false
		for i := range f.Stmts {
			st := &f.Stmts[i]
			if r, ok := config.StmtRecord(st); ok && r.Name == name {
				rec = config.RecordSpec{Name: r.Name, Interval: r.Interval, Type: r.Type, Target: r.Target, Opts: r.Opts, Comment: st.Comment}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("record %s not found", name)
		}
		var opts []config.Opt
		for _, o := range rec.Opts {
			if o.Key != "disabled" {
				opts = append(opts, o)
			}
		}
		if paused {
			opts = append(opts, config.Opt{Key: "disabled", Value: "yes"})
		}
		rec.Opts = opts
		if err := f.UpsertRecord(rec); err != nil {
			return err
		}
	}
	return nil
}

// IsMember reports whether this server is a node of a cluster, running
// its master's configuration.
func (a *App) IsMember() bool {
	a.am.mu.Lock()
	defer a.am.mu.Unlock()
	return a.am.member != nil
}
