package server

import (
	"bytes"
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
// "classic" has its own styles/<id>.css. The only styles are the four
// scenario styles (office, datacenter, retail, noc), and they are
// registered — with a free trial: an unregistered installation may use
// them for seven days from the first time one is switched on (see
// app.StyleTrial); while the trial runs or has not started they can be
// picked and set as the default, and once it has ended a stored choice
// of one falls back to "classic" everywhere it is applied and setting
// one is refused. A stored choice naming a style that is not there
// (an older style) falls back to "classic" the same way.

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

// styleAllowed reports whether the style can be picked here: it must
// exist, and a registered style needs a registered installation or a
// trial that has not ended.
func (s *Server) styleAllowed(id string) bool {
	for _, st := range s.styles() {
		if st.ID == id {
			if !st.Registered || s.app.License().State().Registered {
				return true
			}
			return !s.app.StyleTrial().Ended
		}
	}
	return false
}

// styleApplies reports whether the style takes effect here: a
// registered style needs a registered installation or a trial that is
// running (a trial that was never started applies nothing).
func (s *Server) styleApplies(id string) bool {
	for _, st := range s.styles() {
		if st.ID == id {
			if !st.Registered || s.app.License().State().Registered {
				return true
			}
			return s.app.StyleTrial().Active
		}
	}
	return false
}

// styleTrialJSON is the style trial as the console shows it.
func (s *Server) styleTrialJSON() map[string]any {
	if s.app.License().State().Registered {
		return map[string]any{"state": "registered"}
	}
	switch t := s.app.StyleTrial(); {
	case t.Active:
		return map[string]any{"state": "active", "days_left": t.DaysLeft}
	case t.Ended:
		return map[string]any{"state": "ended"}
	default:
		return map[string]any{"state": "available"}
	}
}

// defaultStyle is the style for everyone who has not chosen one, and for
// the public status pages.
func (s *Server) defaultStyle() string {
	v, _ := s.app.Store().Setting(styleDefaultKey)
	if v == "" || !s.styleApplies(v) {
		return styleClassic
	}
	return v
}

// brandPublic is what the sign-in page and the screens without an
// account need: the name to show and the default style.
func (s *Server) brandPublic(w http.ResponseWriter, r *http.Request) {
	list := []map[string]any{}
	for _, st := range s.styles() {
		list = append(list, map[string]any{"id": st.ID, "allowed": s.styleAllowed(st.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": s.app.BrandName(), "style": s.defaultStyle(), "styles": list, "style_trial": s.styleTrialJSON()})
}

func (s *Server) brandGet(w http.ResponseWriter, r *http.Request) (any, error) {
	b := s.app.BrandSettings()
	return map[string]any{"name": b.Name, "logo": b.Logo, "icon": b.Icon, "allowed": b.Allowed,
		"style": s.defaultStyle(), "product": app.ProductName, "style_trial": s.styleTrialJSON()}, nil
}

func (s *Server) brandPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name  *string `json:"name"`
		Style *string `json:"style"`
	}
	if err := decodeJSON(r, 4<<10, &req); err != nil {
		return nil, err
	}
	// The name is the brand itself: writing it needs a registered
	// installation, like the images. The default style stays free,
	// unless the style picked is itself a registered one.
	if req.Name != nil && !s.app.License().State().Registered {
		return nil, registrationError(s)
	}
	if req.Style != nil {
		v := *req.Style
		if v != styleClassic && !s.styleKnown(v) {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no style %q", v)
		}
		if !s.styleAllowed(v) {
			return nil, registrationError(s)
		}
		if s.styleRegistered(v) && !s.app.License().State().Registered {
			// Switching a registered style on starts its free trial.
			s.app.StartStyleTrial()
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

// serveStyledShell serves one of the shells whose script does not load
// a style itself (the wall screen, the managers' summary) with the
// installation's default style added: the data-style attribute and the
// style sheet, as serveStatusHTML does for the public status pages. A
// classic default leaves the shell unchanged, and so does a style this
// installation may not use (defaultStyle already falls back then).
// Like serveShell it also injects the <base> for the request's honoured
// path prefix, so the shells work behind a prefix proxy too.
// False means the file is not there and the caller should fall back to
// plain file serving.
func (s *Server) serveStyledShell(w http.ResponseWriter, r *http.Request, name string) bool {
	page, err := fs.ReadFile(s.opts.Web, name)
	if err != nil {
		return false
	}
	if st := s.defaultStyle(); st != styleClassic {
		// The shells do not all spell the app.css link the same way:
		// the wall and the summary use the absolute form.
		for _, link := range []string{`<link rel="stylesheet" href="app.css">`, `<link rel="stylesheet" href="/app.css">`} {
			if bytes.Contains(page, []byte(link)) {
				sheet := strings.Replace(link, "app.css", "styles/"+st+".css", 1)
				page = bytes.Replace(page, []byte(link), []byte(link+"\n"+sheet), 1)
				break
			}
		}
		if i := bytes.Index(page, []byte("<html")); i >= 0 {
			if j := bytes.IndexByte(page[i:], '>'); j >= 0 {
				attr := []byte(` data-style="` + st + `"`)
				out := make([]byte, 0, len(page)+len(attr))
				out = append(out, page[:i+j]...)
				out = append(out, attr...)
				out = append(out, page[i+j:]...)
				page = out
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(injectBase(page, s.urlPrefix(r)))
	return true
}

// styleRegistered reports whether the style is one of the registered
// ones.
func (s *Server) styleRegistered(id string) bool {
	for _, st := range s.styles() {
		if st.ID == id {
			return st.Registered
		}
	}
	return false
}

func (s *Server) styleKnown(id string) bool {
	for _, st := range s.styles() {
		if st.ID == id {
			return true
		}
	}
	return false
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
// did.
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
		if !s.styleApplies(strings.TrimSuffix(id, ".css")) {
			writeError(w, notFound("style"))
			return true
		}
	}
	return false
}
