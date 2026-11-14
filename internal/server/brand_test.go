package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/githubflyideas/unagikuma/internal/license"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(1, 1, color.RGBA{200, 10, 10, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// raw sends a body as it is and returns the status and reply.
func (e *env) raw(method, path string, body []byte, ctype string) (int, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if e.csrf != "" {
		req.Header.Set("X-Unagikuma-CSRF", e.csrf)
	}
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

var brandUI = fstest.MapFS{
	"index.html":         {Data: []byte("<html></html>")},
	"logo.png":           {Data: []byte("product logo")},
	"favicon.png":        {Data: []byte("product icon")},
	"styles/styles.json": {Data: []byte(`[{"id": "modern"}, {"id": "harbour", "registered": true}]`)},
	"styles/modern.css":  {Data: []byte(":root[data-style=modern] {}")},
	"styles/harbour.css": {Data: []byte(":root[data-style=harbour] {}")},
}

// A brand can be filled in before registration but shows only after it;
// a registered-only style is neither offered nor served before.
func TestBranding(t *testing.T) {
	e := newEnv(t, testBind, brandUI)
	e.setup()

	var pub struct {
		Name   string
		Style  string
		Styles []struct {
			ID         string
			Registered bool
			Allowed    bool
		}
	}
	if c := e.do("GET", "/api/v1/brand", nil, &pub); c != 200 || pub.Name != "Unagikuma" || pub.Style != "classic" || len(pub.Styles) != 3 || pub.Styles[2].Allowed {
		t.Fatalf("public: %d %+v", c, pub)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"name": "Harbour Ops"}, nil); c != 200 {
		t.Fatalf("name: %d", c)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"name": "<b>x</b>"}, nil); c != 400 {
		t.Fatalf("markup in the name: %d", c)
	}
	logo := pngOf(t, 64, 64)
	if c, b := e.raw("PUT", "/api/v1/branding/logo", logo, "image/png"); c != 200 {
		t.Fatalf("logo: %d %s", c, b)
	}
	if c, _ := e.raw("PUT", "/api/v1/branding/logo", []byte(`<svg onload="alert(1)"/>`), "image/svg+xml"); c != 400 {
		t.Fatalf("svg: %d", c)
	}
	if c, _ := e.raw("PUT", "/api/v1/branding/icon", pngOf(t, 4, 4), "image/png"); c != 400 {
		t.Fatalf("tiny icon: %d", c)
	}
	// Not registered: the product's name, logo and styles stay.
	if c, b := e.raw("GET", "/logo.png", nil, ""); c != 200 || string(b) != "product logo" {
		t.Fatalf("logo before registering: %d %q", c, b)
	}
	if c, b := e.raw("GET", "/api/v1/branding/logo/preview", nil, ""); c != 200 || !bytes.Equal(b, logo) {
		t.Fatalf("preview: %d", c)
	}
	_ = e.do("GET", "/api/v1/brand", nil, &pub)
	if pub.Name != "Unagikuma" {
		t.Fatalf("name before registering: %q", pub.Name)
	}
	if c, _ := e.raw("GET", "/styles/harbour.css", nil, ""); c != 404 {
		t.Fatalf("registered style served: %d", c)
	}
	if c, _ := e.raw("GET", "/styles/modern.css", nil, ""); c != 200 {
		t.Fatalf("free style: %d", c)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "harbour"}, nil); c != 402 {
		t.Fatalf("registered style as default: %d", c)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "nope"}, nil); c != 400 {
		t.Fatalf("unknown style: %d", c)
	}

	e.register(license.FeatBranding)
	if c, b := e.raw("GET", "/logo.png", nil, ""); c != 200 || !bytes.Equal(b, logo) {
		t.Fatalf("logo after registering: %d", c)
	}
	_ = e.do("GET", "/api/v1/brand", nil, &pub)
	if pub.Name != "Harbour Ops" || !pub.Styles[2].Allowed {
		t.Fatalf("after registering: %+v", pub)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "harbour"}, nil); c != 200 {
		t.Fatalf("default style: %d", c)
	}
	if c, _ := e.raw("GET", "/styles/harbour.css", nil, ""); c != 200 {
		t.Fatalf("registered style: %d", c)
	}
	if name := e.app.BrandName(); name != "Harbour Ops" {
		t.Fatalf("notification name: %q", name)
	}
	if c, _ := e.raw("DELETE", "/api/v1/branding/logo", nil, ""); c != 200 {
		t.Fatalf("remove: %d", c)
	}
	if _, b := e.raw("GET", "/logo.png", nil, ""); !strings.Contains(string(b), "product logo") {
		t.Fatalf("logo after removing: %q", b)
	}
}
