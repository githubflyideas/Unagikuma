package server

import (
	"net/http"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Role rights: a viewer reads; an operator also acknowledges and edits
// monitors and maintenance; an admin does everything. An operator limited
// to some $SCOPE sections edits only what is written in them.
func roleScopes(role string) map[string]bool {
	switch role {
	case store.RoleViewer:
		return map[string]bool{"read": true}
	case store.RoleOperator:
		return map[string]bool{"read": true, "ack": true, "write": true}
	}
	return map[string]bool{"read": true, "ack": true, "write": true, "admin": true}
}

// restricted reports an account that may not change shared configuration:
// one limited to some sections, or one that works within its teams.
func (ai *authInfo) restricted() bool { return len(ai.areas) > 0 || ai.team != nil }

// allowsArea reports whether the caller may change things in a section
// ("" is the global section).
func (ai *authInfo) allowsArea(area string) bool {
	if len(ai.areas) == 0 {
		return true
	}
	for _, a := range ai.areas {
		if a == area {
			return true
		}
	}
	return false
}

var errOutsideAreas = Errorf(http.StatusForbidden, CodeForbidden, "your account may only change monitors in its own sections")

// allowsMonitor reports whether the caller may change a monitor.
func (s *Server) allowsMonitor(ai *authInfo, name string) bool {
	if !ai.restricted() {
		return true
	}
	m := s.app.EffectiveConfig().FindMonitor(name)
	return m != nil && ai.allowsArea(m.Area) && (ai.team == nil || ai.team.writes(m.Team))
}

// allowsIncident reports whether the caller may act on an incident.
func (s *Server) allowsIncident(ai *authInfo, id int64) bool {
	if !ai.restricted() {
		return true
	}
	for _, inc := range s.app.Engine().OpenIncidents() {
		if inc.ID == id {
			return s.allowsMonitor(ai, inc.Monitor)
		}
	}
	return false
}

// global wraps handlers that change configuration shared by every
// section: section-limited accounts may not use them.
func global(f apiFunc) apiFunc {
	return func(w http.ResponseWriter, r *http.Request) (any, error) {
		if ai := authOf(r); ai != nil && ai.restricted() {
			return nil, Errorf(http.StatusForbidden, CodeForbidden, "your account is limited to some sections and cannot change shared configuration")
		}
		return f(w, r)
	}
}

// allowsMonitors reports whether the caller may change all of them.
func (s *Server) allowsMonitors(ai *authInfo, names []string) bool {
	for _, n := range names {
		if !s.allowsMonitor(ai, n) {
			return false
		}
	}
	return true
}

var errExecAdmin = Errorf(http.StatusForbidden, CodeForbidden, "only administrators may add or change EXEC checks: they run programs on the probe nodes")

// execDefs maps every EXEC monitor to what it runs and where.
func execDefs(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for _, m := range cfg.Monitors {
		if m.Type == "EXEC" {
			out[m.Name] = strings.Join([]string{m.Target, m.Opts["args"], m.Opts["address"], strings.Join(m.Nodes, ",")}, "\x00")
		}
		// An event handler runs a script on this server as well.
		if h := m.Opts["handler"]; h != "" {
			out[m.Name+"\x00handler"] = h
		}
	}
	return out
}

// execAllowed lets only administrators add EXEC checks or change what
// one runs; operators may still pause, acknowledge or delete them.
func (s *Server) execAllowed(ai *authInfo, text []byte) error {
	if ai == nil || ai.scopes["admin"] {
		return nil
	}
	// The same validation as saving: a text with errors is rejected there.
	next, diags := s.app.ValidateConfig(text)
	if next == nil || config.HasErrors(diags) {
		return nil
	}
	cur := execDefs(s.app.EffectiveConfig())
	for name, def := range execDefs(next) {
		if cur[name] != def {
			return errExecAdmin
		}
	}
	return nil
}

// guardExec applies execAllowed to a structured edit.
func (s *Server) guardExec(ai *authInfo, change func(*config.File) error) func(*config.File) error {
	return func(f *config.File) error {
		if err := change(f); err != nil {
			return err
		}
		return s.execAllowed(ai, f.Bytes())
	}
}
