package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/githubflyideas/unagikuma/internal/postmortem"
)

// incidentReport is the postmortem of one incident: JSON for the web
// page, or a Markdown/HTML/JSON file to keep (download=1).
func (s *Server) incidentReport(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if !s.seesIncident(r, id) {
		return nil, notFound("incident")
	}
	if teamOf(r) == nil && !s.allowsIncident(authOf(r), id) {
		return nil, errOutsideAreas
	}
	rep, err := s.app.PostmortemFor(id, r.URL.Query().Get("lang"), teamOf(r) != nil)
	if errors.Is(err, postmortem.ErrNotFound) {
		return nil, notFound("incident")
	}
	if err != nil {
		return nil, err
	}
	format := r.URL.Query().Get("format")
	if format == "" && r.URL.Query().Get("download") == "" {
		return rep, nil
	}
	if format == "" {
		format = "md"
	}
	if !slices.Contains(postmortem.Formats, format) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "format must be md, json or html")
	}
	ct := map[string]string{"md": "text/markdown; charset=utf-8", "json": "application/json", "html": "text/html; charset=utf-8"}[format]
	w.Header().Set("Content-Type", ct)
	if r.URL.Query().Get("download") != "" || format != "html" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="incident-%d.%s"`, id, format))
	}
	if format == "html" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	}
	if err := rep.Write(w, format); err != nil {
		s.log.Error("incident report", "err", err)
	}
	return nil, errHandled
}
