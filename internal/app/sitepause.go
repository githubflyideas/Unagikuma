package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Site-local pause: pausing or resuming a monitor on this node stops or
// starts checks and alerts here only. It is stored in its own file, never
// sent to other nodes, never part of the master's state copy, and neither
// the master's sync, a join nor a promotion overwrites it.
//
// The overlay is a map of monitor name → wanted pause. true forces a
// pause; false forces a run on this site (overrides shared disabled=yes
// without writing monitors.conf). Absent means follow the shared
// disabled= flag. Master and replica behave the same: pause/resume never
// edits monitors.conf and never propagates to other sites.

const sitePausesFile = "site-pauses.json"

func (a *App) sitePausesPath() string { return a.dataPath(sitePausesFile) }

func (a *App) loadSitePauses() map[string]bool {
	b, err := os.ReadFile(a.sitePausesPath())
	if err != nil {
		return map[string]bool{}
	}
	var asMap map[string]bool
	if err := json.Unmarshal(b, &asMap); err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(asMap))
	for k, v := range asMap {
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func (a *App) saveSitePauses(paused map[string]bool) error {
	if len(paused) == 0 {
		_ = os.Remove(a.sitePausesPath())
		return nil
	}
	keys := make([]string, 0, len(paused))
	for n := range paused {
		keys = append(keys, n)
	}
	sort.Strings(keys)
	ordered := make(map[string]bool, len(paused))
	for _, k := range keys {
		ordered[k] = paused[k]
	}
	b, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writePeerAtomic(a.sitePausesPath(), b)
}

// LocalPaused reports whether this site's overlay currently forces a pause.
func (a *App) LocalPaused(name string) bool {
	v, ok := a.loadSitePauses()[name]
	return ok && v
}

// EffectivelyPaused is the pause state this site applies.
func (a *App) EffectivelyPaused(name string) bool {
	cfg := a.EffectiveConfig()
	disabled := false
	if cfg != nil {
		if m := cfg.FindMonitor(name); m != nil {
			disabled = m.Disabled
		}
	}
	if v, ok := a.loadSitePauses()[name]; ok {
		return v
	}
	return disabled
}

// applySitePauses applies the site-local pause overlay onto the engine config.
func (a *App) applySitePauses(ec *engine.Config) {
	overlay := a.loadSitePauses()
	if len(overlay) == 0 {
		return
	}
	for i := range ec.Monitors {
		if v, ok := overlay[ec.Monitors[i].Name]; ok {
			ec.Monitors[i].Paused = v
		}
	}
}

// SetLocalPaused pauses or resumes monitors on this site only.
// It never writes monitors.conf.
func (a *App) SetLocalPaused(names []string, paused bool, actor string) (changed int, err error) {
	if len(names) == 0 {
		return 0, nil
	}
	if len(names) > 1000 {
		return 0, fmt.Errorf("too many monitors")
	}
	cfg := a.EffectiveConfig()
	if cfg == nil {
		return 0, errors.New("no configuration")
	}
	for _, n := range names {
		if cfg.FindMonitor(n) == nil {
			return 0, engine.ErrNotFound
		}
	}

	a.sitePauseMu.Lock()
	cur := a.loadSitePauses()
	var did []string
	for _, n := range names {
		m := cfg.FindMonitor(n)
		disabled := m != nil && m.Disabled
		if effectivelyPausedOf(cur, n, disabled) == paused {
			continue
		}
		if paused {
			cur[n] = true
		} else if disabled {
			// Force a run here without clearing shared disabled=.
			cur[n] = false
		} else {
			delete(cur, n)
		}
		did = append(did, n)
	}
	if len(did) > 0 {
		if err = a.saveSitePauses(cur); err != nil {
			a.sitePauseMu.Unlock()
			return 0, err
		}
	}
	a.sitePauseMu.Unlock()

	if len(did) == 0 {
		return 0, nil
	}
	a.reapply()
	for _, n := range did {
		a.emit("state", map[string]any{"name": n})
	}
	verb := "resume"
	if paused {
		verb = "pause"
	}
	_ = a.store.Audit(store.AuditEntry{
		Actor:  actor,
		Action: "monitor." + verb,
		Target: strings.Join(did, ", "),
		Detail: map[string]any{"site_local": true, "paused": paused},
	})
	return len(did), nil
}

func effectivelyPausedOf(cur map[string]bool, name string, disabled bool) bool {
	if v, ok := cur[name]; ok {
		return v
	}
	return disabled
}
