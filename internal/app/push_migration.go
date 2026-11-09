package app

import (
	"github.com/githubflyideas/unagikuma/internal/config"
	"os"
)

// Previous versions ignored PUSH node scopes. Preserve that receiver during
// the one-time upgrade rather than creating a deadline at every site.
func (a *App) migratePushReceivers(path string) error {
	if done, _ := a.store.Setting("per_site_schema"); done == "1" {
		return nil
	}
	text, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	f, c, _ := config.Load(path, text, config.Options{})
	if c == nil {
		return nil
	}
	changed := false
	for _, m := range c.Monitors {
		if m.Type != "PUSH" || len(m.Nodes) <= 1 {
			continue
		}
		r, ok := f.Record(m.Name)
		if !ok {
			continue
		}
		opts := []config.Opt{}
		for _, o := range r.Opts {
			if o.Key != "nodes" {
				opts = append(opts, o)
			}
		}
		opts = append(opts, config.Opt{Key: "nodes", Value: c.MName})
		if err := f.UpsertRecord(config.RecordSpec{Name: r.Name, Type: r.Type, Target: r.Target, Interval: r.Interval, Opts: opts}); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	serial, _, err := config.NextSerial(c.Serial, a.now())
	if err != nil {
		return err
	}
	if err := f.SetSerial(serial); err != nil {
		return err
	}
	if _, _, d := config.Load(path, f.Bytes(), a.validators()); config.HasErrors(d) {
		return nil
	}
	backup, err := os.OpenFile(path+".before-per-site", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
	return a.store.Audit(auditEntry("config.push_receiver_migration", "system", map[string]any{"file": path, "receiver": c.MName}))
}
