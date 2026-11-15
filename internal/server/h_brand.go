package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/app"
)

// Styles are built-in layouts of the console: how pages are laid out
// (cards, tables, spacing, type), independent of the colour theme. They
// are listed in styles/styles.json of the web files; every style but
// "classic" has its own styles/<id>.css. A style marked "registered" is
// served to registered installations only.

const (
	styleClassic    = "classic"
	styleDefaultKey = "ui.style"
)

type styleInfo struct {
	ID         string `json:"id"`
	Registered bool   `json:"registered,omitempty"`
}

func (s *Server) styles() []styleInfo {
	list := []styleInfo{{ID: styleClassic}}
	if s.opts.Web == nil {
		return list
	}
	b, err := fs.ReadFile(s.opts.Web, "styles/styles.json")
	if err != nil {
		return list
	}
	var all []styleInfo
	if json.Unmarshal(b, &all) != nil {
		return list
	}
	for _, st := range all {
		if st.ID != "" && st.ID != styleClassic && validStyleID(st.ID) {
			list = append(list, st)
		}
	}
	return list
}

func validStyleID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// styleAllowed reports whether this installation may use the style.
func (s *Server) styleAllowed(id string) bool {
	for _, st := range s.styles() {
		if st.ID == id {
			return !st.Registered || s.app.License().State().Registered
		}
	}
	return false
}

// defaultStyle is the style for everyone who has not chosen one, and for
// the public status pages.
func (s *Server) defaultStyle() string {
	v, _ := s.app.Store().Setting(styleDefaultKey)
	if v == "" || !s.styleAllowed(v) {
		return styleClassic
	}
	return v
}

// brandPublic is what the sign-in page and the screens without an
// account need: the name to show and the default style.
func (s *Server) brandPublic(w http.ResponseWriter, r *http.Request) {
	reg := s.app.License().State().Registered
	list := []map[string]any{}
	for _, st := range s.styles() {
		list = append(list, map[string]any{"id": st.ID, "registered": st.Registered, "allowed": !st.Registered || reg})
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": s.app.BrandName(), "style": s.defaultStyle(), "styles": list})
}

func (s *Server) brandGet(w http.ResponseWriter, r *http.Request) (any, error) {
	b := s.app.BrandSettings()
	return map[string]any{"name": b.Name, "logo": b.Logo, "icon": b.Icon, "allowed": b.Allowed,
		"style": s.defaultStyle(), "product": app.ProductName}, nil
}

func (s *Server) brandPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name  *string `json:"name"`
		Style *string `json:"style"`
	}
	if err := decodeJSON(r, 4<<10, &req); err != nil {
		return nil, err
	}
	if req.Style != nil {
		v := *req.Style
		if v != styleClassic && !s.styleAllowed(v) {
			if validStyleID(v) && s.styleKnown(v) {
				return nil, requireRegistration(s)
			}
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no style %q", v)
		}
		if err := s.app.Store().SetSetting(styleDefaultKey, v); err != nil {
			return nil, err
		}
		auditOf(r).note("style", v)
	}
	if req.Name != nil {
		if err := s.app.SetBrandName(*req.Name); err != nil {
			return nil, brandError(err)
		}
		auditOf(r).note("name", strings.TrimSpace(*req.Name))
	}
	auditOf(r).target = "branding"
	return s.brandGet(w, r)
}

func (s *Server) styleKnown(id string) bool {
	for _, st := range s.styles() {
		if st.ID == id {
			return true
		}
	}
	return false
}

func requireRegistration(s *Server) error {
	e := Errorf(http.StatusPaymentRequired, CodeRegistration, "this style needs a registered installation (Settings › Registration)")
	e.Extra = map[string]any{"feature": "style", "installation_id": s.app.License().State().InstallationID}
	return e
}

func brandError(err error) error {
	if errors.Is(err, app.ErrBrand) {
		msg := err.Error()
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return Errorf(http.StatusBadRequest, CodeInvalid, "%s", msg)
	}
	return err
}

func brandImageOf(r *http.Request) (app.BrandImage, error) {
	switch r.PathValue("image") {
	case "logo":
		return app.BrandLogo, nil
	case "icon":
		return app.BrandIcon, nil
	}
	return "", notFound("image")
}

// brandImagePut stores an uploaded image; the body is the file itself.
func (s *Server) brandImagePut(w http.ResponseWriter, r *http.Request) (any, error) {
	k, err := brandImageOf(r)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return nil, Errorf(http.StatusRequestEntityTooLarge, CodeTooLarge, "the image is too large")
	}
	if err := s.app.SetBrandImage(k, b); err != nil {
		return nil, brandError(err)
	}
	auditOf(r).note("image", string(k)).target = "branding"
	return s.brandGet(w, r)
}

func (s *Server) brandImageDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	k, err := brandImageOf(r)
	if err != nil {
		return nil, err
	}
	if err := s.app.SetBrandImage(k, nil); err != nil {
		return nil, err
	}
	auditOf(r).note("image", string(k)+" removed").target = "branding"
	return s.brandGet(w, r)
}

// brandImagePreview shows a stored image on the settings page, also
// before the installation is registered.
func (s *Server) brandImagePreview(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		writeError(w, err)
		return
	}
	k, err := brandImageOf(r)
	if err != nil {
		writeError(w, err)
		return
	}
	b, ok := s.app.StoredBrandImage(k)
	if !ok {
		writeError(w, notFound("image"))
		return
	}
	writeImage(w, b)
}

func writeImage(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}

// serveBranded answers the paths a brand replaces; it reports whether it
// did. A style a registered-only listing names is not served without
// registration.
func (s *Server) serveBranded(w http.ResponseWriter, r *http.Request, p string) bool {
	switch p {
	case "logo.png":
		if b, ok := s.app.BrandImageData(app.BrandLogo); ok {
			writeImage(w, b)
			return true
		}
	case "favicon.png":
		if b, ok := s.app.BrandImageData(app.BrandIcon); ok {
			writeImage(w, b)
			return true
		}
	}
	if id, ok := strings.CutPrefix(p, "styles/"); ok && strings.HasSuffix(id, ".css") {
		if !s.styleAllowed(strings.TrimSuffix(id, ".css")) {
			writeError(w, notFound("style"))
			return true
		}
	}
	return false
}
