package app

import (
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// sink persists through the store and broadcasts live updates.
type sink struct {
	*store.Store
	a *App
}

func (s *sink) SaveState(r engine.StateRecord) {
	s.Store.SaveState(r)
	s.a.emit("state", map[string]any{"name": r.Name})
}

func (s *sink) SaveIncident(inc *engine.Incident) error {
	err := s.Store.SaveIncident(inc)
	if err == nil {
		s.a.emit("incident", map[string]any{"id": inc.ID, "monitor": inc.Monitor})
	}
	return err
}

func (s *sink) Event(ev engine.Event) {
	s.Store.Event(ev)
	s.a.emit("event", ev)
}

func (s *sink) Verdict(v engine.VerdictRecord) {
	s.Store.Verdict(v)
	s.a.emit("heartbeat", map[string]any{"name": v.Monitor, "cell": map[string]any{"t": v.At, "s": v.Status, "n": 1}})
}
