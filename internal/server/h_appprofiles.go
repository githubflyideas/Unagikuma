package server

import (
	"net/http"

	"github.com/githubflyideas/unagikuma/internal/appprofile"
)

// appProfiles lists the applications an APP monitor can watch, with the
// numbers each has, for the monitor form.
func (s *Server) appProfiles(w http.ResponseWriter, r *http.Request) (any, error) {
	type field struct {
		Key     string `json:"key"`
		Label   string `json:"label"`
		Type    string `json:"type"`
		Unit    string `json:"unit,omitempty"`
		Primary bool   `json:"primary,omitempty"`
	}
	type profile struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Reader   string  `json:"reader"`
		Path     string  `json:"path"`
		Port     int     `json:"port"`
		Docs     string  `json:"docs"`
		Needs    string  `json:"needs"`
		Verified string  `json:"verified"`
		Fields   []field `json:"fields"`
	}
	out := []profile{}
	for _, p := range appprofile.All() {
		pr := profile{ID: p.ID, Title: p.Title, Reader: p.Reader, Path: p.Path, Port: p.Port, Docs: p.Docs, Needs: p.Needs, Verified: p.Verified}
		for _, f := range p.Fields {
			pr.Fields = append(pr.Fields, field{f.Key, f.Label, f.Type, f.Unit, f.Key == p.Primary})
		}
		out = append(out, pr)
	}
	return out, nil
}
