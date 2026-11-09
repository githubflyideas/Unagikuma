package server

import (
	"encoding/json"
	"net/http"

	"github.com/githubflyideas/unagikuma/internal/cluster"
)

// agentAllowed rate-limits a node's request by the login limiter, but only
// when its token does not authenticate: a token is a credential and must
// not be guessable at network speed, while a node using its own must not
// share a budget of five a minute with the people signing in from the same
// address — its connects and state copies would be refused, a standby's
// copy right after a burst of configuration changes above all.
func (s *Server) agentAllowed(w http.ResponseWriter, r *http.Request) bool {
	if m := s.app.Cluster(); m != nil {
		if _, err := m.AuthAgent(r); err == nil {
			return true
		}
	}
	if ok, wait := s.login.Allow(s.ip(r), s.now()); !ok {
		writeError(w, rateLimited(wait))
		return false
	}
	return true
}

// agentConnect upgrades a node's connection.
func (s *Server) agentConnect(w http.ResponseWriter, r *http.Request) {
	if s.agentAllowed(w, r) {
		s.app.Cluster().ServeHTTP(w, r)
	}
}

// agentState serves the master's state to a standby node.
func (s *Server) agentState(w http.ResponseWriter, r *http.Request) {
	if s.agentAllowed(w, r) {
		s.app.Cluster().ServeState(w, r)
	}
}

// agentRetire takes the signed notice of the master that replaced this
// one: it steps down.
func (s *Server) agentRetire(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.login.Allow(s.ip(r), s.now()); !ok {
		writeError(w, rateLimited(wait))
		return
	}
	if r.TLS == nil {
		writeError(w, Errorf(http.StatusForbidden, CodeForbidden, "TLS required"))
		return
	}
	var n cluster.RetireNotice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&n); err != nil {
		writeError(w, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err))
		return
	}
	if err := s.app.AcceptRetire(n); err != nil {
		writeError(w, Errorf(http.StatusConflict, CodeConflict, "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
