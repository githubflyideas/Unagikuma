// Package branding reads, stores and serves theme packs: a colour set for
// the console, optionally with a product name and logo (white label).
//
// A pack is a zip of theme.json plus optional logo and favicon images. It
// carries data only: colours are checked against the console's colour
// tokens, images must be PNG or JPEG, and no CSS, SVG or script is ever
// taken from it, so a pack cannot run code in the console.
package branding

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // logos may be JPEG
	_ "image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Limits of an uploaded pack.
const (
	MaxZip   = 2 << 20   // the whole upload
	MaxImage = 512 << 10 // one image
	MaxSide  = 2048      // image width and height in pixels
)

// Tokens are the colour names a pack may set (the console's CSS
// variables without the leading "--").
var Tokens = []string{
	"bg", "side", "panel", "panel-2", "border", "border-strong",
	"text", "muted", "faint", "accent", "accent-soft", "accent-text", "link",
	"ok", "ok-soft", "degraded", "degraded-soft", "critical", "critical-soft",
	"unknown", "unknown-soft", "pending", "pending-soft", "maint", "maint-soft",
	"on-status", "on-degraded", "empty",
}

// Layouts a pack may ask for. "" and "standard" are the usual console;
// the others are the duty, wall and executive presets.
var Layouts = []string{"", "standard", "duty", "wall", "exec"}

// Brand is the white-label part of a pack.
type Brand struct {
	Product     string `json:"product,omitempty"`      // replaces "Unagikuma" in the console and messages
	Short       string `json:"short,omitempty"`        // a short form for narrow places
	HidePowered bool   `json:"hide_powered,omitempty"` // leave out "Powered by" on status pages
}

// Pack is theme.json plus what the zip held.
type Pack struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Author     string            `json:"author,omitempty"`
	Version    string            `json:"version,omitempty"`
	Base       string            `json:"base"` // light or dark: what unset colours fall back to
	Layout     string            `json:"layout,omitempty"`
	Brand      Brand             `json:"brand"`
	Colors     map[string]string `json:"colors"`
	HasLogo    bool              `json:"has_logo"`
	HasFavicon bool              `json:"has_favicon"`
}

// manifest is theme.json as written by people.
type manifest struct {
	Name    string            `json:"name"`
	Author  string            `json:"author"`
	Version string            `json:"version"`
	Base    string            `json:"base"`
	Layout  string            `json:"layout"`
	Brand   Brand             `json:"brand"`
	Colors  map[string]string `json:"colors"`
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Image file names inside a pack, and the names they are stored under.
const (
	fileManifest = "theme.json"
	fileLogo     = "logo"
	fileFavicon  = "favicon"
)

// Parse reads and checks an uploaded pack. It returns the pack and its
// images (logo, favicon) as stored: the original bytes, already decoded
// once to prove they are PNG or JPEG of a sensible size.
func Parse(data []byte) (*Pack, map[string][]byte, error) {
	if len(data) == 0 || len(data) > MaxZip {
		return nil, nil, fmt.Errorf("a theme pack is a zip of at most %d KiB", MaxZip>>10)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, errors.New("not a zip file")
	}
	if len(zr.File) > 16 {
		return nil, nil, errors.New("too many files in the pack")
	}
	files := map[string][]byte{}
	prefix := commonDir(zr.File)
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, prefix)
		if f.FileInfo().IsDir() || name == "" || strings.HasPrefix(path.Base(name), ".") || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		key, ok := packFile(name)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected file %q: a pack holds theme.json, logo.png (or .jpg) and favicon.png only", name)
		}
		if _, dup := files[key]; dup {
			return nil, nil, fmt.Errorf("two files for %s", key)
		}
		if f.UncompressedSize64 > MaxImage {
			return nil, nil, fmt.Errorf("%s is larger than %d KiB", name, MaxImage>>10)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", name, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, MaxImage+1))
		rc.Close()
		if err != nil || len(b) > MaxImage {
			return nil, nil, fmt.Errorf("%s cannot be read or is too large", name)
		}
		files[key] = b
	}
	mb, ok := files[fileManifest]
	if !ok {
		return nil, nil, errors.New("theme.json is missing")
	}
	delete(files, fileManifest)
	p, err := parseManifest(mb)
	if err != nil {
		return nil, nil, err
	}
	for key, b := range files {
		if err := checkImage(b); err != nil {
			return nil, nil, fmt.Errorf("%s: %v", key, err)
		}
	}
	_, p.HasLogo = files[fileLogo]
	_, p.HasFavicon = files[fileFavicon]
	return p, files, nil
}

// commonDir is the single top folder every entry is in ("" if none), so a
// pack zipped together with its folder reads the same as one zipped
// from inside it.
func commonDir(fs []*zip.File) string {
	dir := ""
	for i, f := range fs {
		if strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		first, _, found := strings.Cut(f.Name, "/")
		if !found {
			return ""
		}
		if i == 0 || dir == "" {
			dir = first
		} else if first != dir {
			return ""
		}
	}
	if dir == "" {
		return ""
	}
	return dir + "/"
}

func packFile(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "theme.json":
		return fileManifest, true
	case "logo.png", "logo.jpg", "logo.jpeg":
		return fileLogo, true
	case "favicon.png":
		return fileFavicon, true
	}
	return "", false
}

func checkImage(b []byte) error {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || (format != "png" && format != "jpeg") {
		return errors.New("must be a PNG or JPEG image")
	}
	if cfg.Width < 16 || cfg.Height < 16 || cfg.Width > MaxSide || cfg.Height > MaxSide {
		return fmt.Errorf("must be between 16 and %d pixels wide and high", MaxSide)
	}
	// Decode it fully once, so a file that only has a valid header is refused.
	if _, _, err := image.Decode(bytes.NewReader(b)); err != nil {
		return errors.New("the image is damaged")
	}
	return nil
}

func label(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl) && strings.TrimSpace(s) == s
}

func parseManifest(b []byte) (*Pack, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var m manifest
	if err := d.Decode(&m); err != nil {
		return nil, fmt.Errorf("theme.json: %v", err)
	}
	if d.More() {
		return nil, errors.New("theme.json: one JSON object expected")
	}
	if m.Name == "" || !label(m.Name, 60) {
		return nil, errors.New("theme.json: name must be 1 to 60 characters")
	}
	if !label(m.Author, 80) || !label(m.Version, 20) {
		return nil, errors.New("theme.json: author is at most 80 characters, version at most 20")
	}
	if m.Base == "" {
		m.Base = "light"
	}
	if m.Base != "light" && m.Base != "dark" {
		return nil, errors.New(`theme.json: base must be "light" or "dark"`)
	}
	if !contains(Layouts, m.Layout) {
		return nil, fmt.Errorf("theme.json: layout must be one of %s", strings.Join(Layouts[1:], ", "))
	}
	if !label(m.Brand.Product, 40) || !label(m.Brand.Short, 12) {
		return nil, errors.New("theme.json: brand.product is at most 40 characters, brand.short at most 12")
	}
	colors := map[string]string{}
	for k, v := range m.Colors {
		if !contains(Tokens, k) {
			return nil, fmt.Errorf("theme.json: unknown colour %q (the colours are: %s)", k, strings.Join(Tokens, ", "))
		}
		if !hexColor.MatchString(v) {
			return nil, fmt.Errorf("theme.json: colour %s must be written #rrggbb", k)
		}
		colors[k] = strings.ToLower(v)
	}
	return &Pack{Name: m.Name, Author: m.Author, Version: m.Version, Base: m.Base, Layout: m.Layout, Brand: m.Brand, Colors: colors}, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Store keeps packs under <data>/themes/<id>/ and remembers the active one.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore opens the pack folder (created when the first pack is added).
func NewStore(dataDir string) *Store { return &Store{dir: filepath.Join(dataDir, "themes")} }

// Dir is where the packs are kept.
func (s *Store) Dir() string { return s.dir }

var idOK = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if b.Len() >= 32 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "theme"
	}
	return id
}

// Add stores a parsed pack under a new id and returns it.
func (s *Store) Add(p *Pack, files map[string][]byte) (*Pack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	base := slug(p.Name)
	id := base
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(s.dir, id)); errors.Is(err, os.ErrNotExist) {
			break
		}
		id = base + "-" + strconv.Itoa(i)
	}
	tmp, err := os.MkdirTemp(s.dir, ".new-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	q := *p
	q.ID = id
	mb, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, fileManifest), mb, 0o644); err != nil {
		return nil, err
	}
	for key, b := range files {
		if err := os.WriteFile(filepath.Join(tmp, key), b, 0o644); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, id)); err != nil {
		return nil, err
	}
	return &q, nil
}

// Get loads one pack.
func (s *Store) Get(id string) (*Pack, error) {
	if !idOK.MatchString(id) {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(s.dir, id, fileManifest))
	if err != nil {
		return nil, err
	}
	var p Pack
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	p.ID = id
	return &p, nil
}

// List returns every stored pack, by name.
func (s *Store) List() []*Pack {
	ents, _ := os.ReadDir(s.dir)
	var out []*Pack
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if p, err := s.Get(e.Name()); err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Image returns a pack's logo or favicon.
func (s *Store) Image(id, which string) ([]byte, error) {
	if !idOK.MatchString(id) || (which != fileLogo && which != fileFavicon) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.dir, id, which))
}

const activeFile = "active"

// Active is the pack in use, or nil for the built-in look.
func (s *Store) Active() *Pack {
	b, err := os.ReadFile(filepath.Join(s.dir, activeFile))
	if err != nil {
		return nil
	}
	p, err := s.Get(strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	return p
}

// SetActive makes a pack the one in use; "" goes back to the built-in look.
func (s *Store) SetActive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		err := os.Remove(filepath.Join(s.dir, activeFile))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if _, err := s.Get(id); err != nil {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, ".active.tmp")
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, activeFile))
}

// Delete removes a pack; the active one cannot be removed.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !idOK.MatchString(id) {
		return os.ErrNotExist
	}
	if a := s.Active(); a != nil && a.ID == id {
		return errors.New("this pack is in use: switch to another look first")
	}
	if _, err := os.Stat(filepath.Join(s.dir, id)); err != nil {
		return os.ErrNotExist
	}
	return os.RemoveAll(filepath.Join(s.dir, id))
}

// Export writes a pack back as a zip that Parse reads again.
func (s *Store) Export(id string) ([]byte, error) {
	p, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	m := manifest{Name: p.Name, Author: p.Author, Version: p.Version, Base: p.Base, Layout: p.Layout, Brand: p.Brand, Colors: p.Colors}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, b []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	if err := add("theme.json", append(mb, '\n')); err != nil {
		return nil, err
	}
	for key, name := range map[string]string{fileLogo: "logo", fileFavicon: "favicon.png"} {
		b, err := s.Image(id, key)
		if err != nil {
			continue
		}
		if key == fileLogo {
			name = "logo.png"
			if _, format, _ := image.DecodeConfig(bytes.NewReader(b)); format == "jpeg" {
				name = "logo.jpg"
			}
		}
		if err := add(name, b); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
