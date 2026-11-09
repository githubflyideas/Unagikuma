package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/githubflyideas/unagikuma/internal/store"
)

// agentMerge takes what a node wrote while it checked on its own: GET
// returns the marks (what is already here), POST adds a batch. The site
// is the authenticated node.
func (s *Server) agentMerge(w http.ResponseWriter, r *http.Request) {
	m := s.app.Cluster()
	if m == nil {
		writeError(w, Errorf(http.StatusConflict, CodeConflict, "this server is not a master"))
		return
	}
	node, err := m.AuthAgent(r)
	if err != nil {
		// Only failures count against the address: a node merging often
		// must not crowd out its own state copies.
		if ok, wait := s.login.Allow(s.ip(r), s.now()); !ok {
			writeError(w, rateLimited(wait))
			return
		}
		writeError(w, Errorf(http.StatusUnauthorized, CodeUnauthorized, "%v", err))
		return
	}
	if r.Method == http.MethodGet {
		marks, err := s.app.MergeMarks(node, r.URL.Query().Get("epoch"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, marks)
		return
	}
	var b store.MergeBatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&b); err != nil {
		writeError(w, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err))
		return
	}
	res, err := s.app.MergeFrom(node, b)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// agentDiverged keeps the configuration a replaced master kept changing.
func (s *Server) agentDiverged(w http.ResponseWriter, r *http.Request) {
	m := s.app.Cluster()
	node, err := m.AuthAgent(r)
	if err != nil {
		if ok, wait := s.login.Allow(s.ip(r), s.now()); !ok {
			writeError(w, rateLimited(wait))
			return
		}
		writeError(w, Errorf(http.StatusUnauthorized, CodeUnauthorized, "%v", err))
		return
	}
	var d store.DivergedConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&d); err != nil {
		writeError(w, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err))
		return
	}
	if err := s.app.AddDiverged(node, d); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// diverged lists kept diverged configurations (with ?id= one with text).
func (s *Server) diverged(w http.ResponseWriter, r *http.Request) (any, error) {
	items, err := s.app.Store().Diverged(r.URL.Query().Get("text") != "")
	return map[string]any{"items": items}, err
}

func (s *Server) divergedDismiss(w http.ResponseWriter, r *http.Request) (any, error) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return map[string]any{"ok": true}, s.app.Store().DismissDiverged(id)
}
