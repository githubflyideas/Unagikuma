package server

import (
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/githubflyideas/unagikuma/internal/branding"
	"github.com/githubflyideas/unagikuma/internal/license"
)

// brandJSON is what every page (the login page included) needs to look
// like the active theme pack. The white-label fields are filled only on a
// registered installation.
type brandJSON struct {
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name,omitempty"`
	Base        string            `json:"base,omitempty"`
	Layout      string            `json:"layout,omitempty"`
	Colors      map[string]string `json:"colors,omitempty"`
	Product     string            `json:"product,omitempty"`
	Short       string            `json:"short,omitempty"`
	Logo        bool              `json:"logo,omitempty"`
	Favicon     bool              `json:"favicon,omitempty"`
	HidePowered bool              `json:"hide_powered,omitempty"`
	WhiteLabel  bool              `json:"white_label"` // the registration allows the product name and logo
}

func (s *Server) brandView() brandJSON {
	p, ok := s.app.Brand()
	if p == nil {
		return brandJSON{}
	}
	b := brandJSON{ID: p.ID, Name: p.Name, Base: p.Base, Layout: p.Layout, Colors: p.Colors, WhiteLabel: ok}
	if ok {
		b.Product, b.Short, b.Logo, b.Favicon, b.HidePowered = p.Brand.Product, p.Brand.Short, p.HasLogo, p.HasFavicon, p.Brand.HidePowered
	}
	return b
}

// brandGet is public: the login page and status pages use it.
func (s *Server) brandGet(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.brandView(), nil
}

// brandImage serves the active pack's logo or favicon in place of the
// built-in one, when white label is allowed. It reports whether it did.
func (s *Server) brandImage(w http.ResponseWriter, r *http.Request, which string) bool {
	p, ok := s.app.Brand()
	if p == nil || !ok || (which == "logo" && !p.HasLogo) || (which == "favicon" && !p.HasFavicon) {
		return false
	}
	b, err := s.app.Themes().Image(p.ID, which)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
	return true
}

type themesJSON struct {
	Packs      []*branding.Pack `json:"packs"`
	Active     string           `json:"active"`
	WhiteLabel bool             `json:"white_label"`
	Tokens     []string         `json:"tokens"`
	Layouts    []string         `json:"layouts"`
}

func (s *Server) themesList(w http.ResponseWriter, r *http.Request) (any, error) {
	out := themesJSON{Packs: s.app.Themes().List(), Tokens: branding.Tokens, Layouts: branding.Layouts[1:]}
	if out.Packs == nil {
		out.Packs = []*branding.Pack{}
	}
	if p := s.app.Themes().Active(); p != nil {
		out.Active = p.ID
	}
	out.WhiteLabel = s.app.License().State().Allowed(license.FeatBranding)
	return out, nil
}

// themesUpload adds a pack sent as a zip body.
func (s *Server) themesUpload(w http.ResponseWriter, r *http.Request) (any, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, branding.MaxZip))
	if err != nil {
		return nil, Errorf(http.StatusRequestEntityTooLarge, CodeTooLarge, "a theme pack is a zip of at most %d KiB", branding.MaxZip>>10)
	}
	p, files, err := branding.Parse(data)
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	saved, err := s.app.Themes().Add(p, files)
	if err != nil {
		return nil, err
	}
	auditOf(r).note("name", saved.Name).target = saved.ID
	return saved, nil
}

// themesActivate switches the look; an empty id goes back to the built-in one.
func (s *Server) themesActivate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, 4<<10, &req); err != nil {
		return nil, err
	}
	if err := s.app.Themes().SetActive(req.ID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, notFound("theme pack")
		}
		return nil, err
	}
	auditOf(r).target = req.ID
	return s.brandView(), nil
}

func (s *Server) themesDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id := r.PathValue("id")
	if err := s.app.Themes().Delete(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, notFound("theme pack")
		}
		return nil, Errorf(http.StatusConflict, CodeConflict, "%v", err)
	}
	auditOf(r).target = id
	return map[string]any{"deleted": id}, nil
}

// themesExport downloads a pack as a zip that can be uploaded elsewhere.
func (s *Server) themesExport(w http.ResponseWriter, r *http.Request) (any, error) {
	id := r.PathValue("id")
	b, err := s.app.Themes().Export(id)
	if err != nil {
		return nil, notFound("theme pack")
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.zip"`)
	w.Write(b)
	return nil, errHandled
}

// themesImage previews a stored pack's logo or favicon in the settings.
func (s *Server) themesImage(w http.ResponseWriter, r *http.Request) (any, error) {
	which := r.PathValue("which")
	b, err := s.app.Themes().Image(r.PathValue("id"), which)
	if err != nil {
		return nil, notFound("image")
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
	return nil, errHandled
}
