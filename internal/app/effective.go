package app

import (
	"github.com/githubflyideas/unagikuma/internal/config"
	"os"
)

// EffectiveConfig is the read-only configuration this site actually runs.
// Config() remains the authority's editable file, never a replica edit target.
func (a *App) EffectiveConfig() *config.Config {
	if a.isMember() {
		a.replica.mu.Lock()
		c := a.replica.cfg
		a.replica.mu.Unlock()
		if c != nil {
			return c
		}
	}
	return a.applied.Load()
}

func (a *App) ConfigAuthority() bool { return !a.isMember() && a.SupersededBy() == nil }

func (a *App) SiteName() string { return a.nodeName() }

func (a *App) EffectiveConfigState() ConfigState {
	cur := a.Config()
	if !a.isMember() {
		return cur
	}
	path := a.dataPath(replicaDir, replicaConfig)
	text, err := os.ReadFile(path)
	if err != nil {
		return cur
	}
	f, c, d := config.Load(path, text, a.validators())
	if c == nil || config.HasErrors(d) {
		return cur
	}
	return ConfigState{Text: string(text), SHA: shaOf(text), Serial: c.Serial, File: f, Config: c}
}
