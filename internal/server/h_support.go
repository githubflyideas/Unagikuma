package server

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/githubflyideas/unagikuma/internal/license"
)

// The line at the foot of the public status pages says "Powered by
// Unagikuma". A registered installation may put its own text there.

const supportSetting = "status.support_text"

// supportText is the footer text in use: "" for the usual line.
func (s *Server) supportText() string {
	if !s.app.License().State().Allowed(license.FeatBranding) {
		return ""
	}
	v, _ := s.app.Store().Setting(supportSetting)
	return v
}

func (s *Server) supportGet(w http.ResponseWriter, r *http.Request) (any, error) {
	v, _ := s.app.Store().Setting(supportSetting)
	return map[string]any{"text": v, "allowed": s.app.License().State().Allowed(license.FeatBranding)}, nil
}

func (s *Server) supportPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, 4<<10, &req); err != nil {
		return nil, err
	}
	req.Text = strings.TrimSpace(req.Text)
	if utf8.RuneCountInString(req.Text) > 120 || strings.ContainsAny(req.Text, "\r\n") {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "the footer text is one line of at most 120 characters")
	}
	if err := s.app.Store().SetSetting(supportSetting, req.Text); err != nil {
		return nil, err
	}
	auditOf(r).note("text", req.Text).target = "status-pages"
	return map[string]any{"text": req.Text, "allowed": true}, nil
}
