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
	"styles/styles.json": {Data: []byte(`[{"id": "modern"}, {"id": "harbour"}]`)},
	"styles/modern.css":  {Data: []byte(":root[data-style=modern] {}")},
	"styles/harbour.css": {Data: []byte(":root[data-style=harbour] {}")},
}

// Branding changes need a registered installation: an unregistered
// write is refused with the registration error and nothing is kept.
// Every style is free: choosing the default style is not branding.
func TestBranding(t *testing.T) {
	e := newEnv(t, testBind, brandUI)
	e.setup()

	var pub struct {
		Name   string
		Style  string
		Styles []struct{ ID string }
	}
	if c := e.do("GET", "/api/v1/brand", nil, &pub); c != 200 || pub.Name != "Unagikuma" || pub.Style != "classic" || len(pub.Styles) != 3 {
		t.Fatalf("public: %d %+v", c, pub)
	}

	// Unregistered: the name and the images are refused, with the same
	// error the other registered-only features serve.
	var er struct{ Code string }
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"name": "Harbour Ops"}, &er); c != 402 || er.Code != CodeRegistration {
		t.Fatalf("name unregistered: %d %+v", c, er)
	}
	// A name bundled with a style is refused as a whole: the style in
	// it is not applied either.
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"name": "Harbour Ops", "style": "harbour"}, nil); c != 402 {
		t.Fatalf("name and style unregistered: %d", c)
	}
	var bg struct {
		Name    string
		Logo    bool
		Allowed bool
		Style   string
	}
	if c := e.do("GET", "/api/v1/branding", nil, &bg); c != 200 || bg.Style != "classic" {
		t.Fatalf("after the refused write: %d %+v", c, bg)
	}
	logo := pngOf(t, 64, 64)
	if c, b := e.raw("PUT", "/api/v1/branding/logo", logo, "image/png"); c != 402 || !strings.Contains(string(b), CodeRegistration) {
		t.Fatalf("logo unregistered: %d %s", c, b)
	}
	if c, _ := e.raw("PUT", "/api/v1/branding/icon", pngOf(t, 64, 64), "image/png"); c != 402 {
		t.Fatalf("icon unregistered: %d", c)
	}
	if c, _ := e.raw("DELETE", "/api/v1/branding/logo", nil, ""); c != 402 {
		t.Fatalf("remove unregistered: %d", c)
	}
	// The product's name, logo and styles stay.
	if c, b := e.raw("GET", "/logo.png", nil, ""); c != 200 || string(b) != "product logo" {
		t.Fatalf("logo before registering: %d %q", c, b)
	}
	_ = e.do("GET", "/api/v1/brand", nil, &pub)
	if pub.Name != "Unagikuma" {
		t.Fatalf("name before registering: %q", pub.Name)
	}
	for _, id := range []string{"modern", "harbour"} {
		if c, _ := e.raw("GET", "/styles/"+id+".css", nil, ""); c != 200 {
			t.Fatalf("style %s unregistered: %d", id, c)
		}
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "harbour"}, nil); c != 200 {
		t.Fatalf("style as default, unregistered: %d", c)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "nope"}, nil); c != 400 {
		t.Fatalf("unknown style: %d", c)
	}

	e.register()
	// Nothing of the refused writes survived registration.
	if c := e.do("GET", "/api/v1/branding", nil, &bg); c != 200 || bg.Name != "" || bg.Logo || !bg.Allowed {
		t.Fatalf("registered, before any write: %d %+v", c, bg)
	}
	if c, b := e.raw("GET", "/logo.png", nil, ""); c != 200 || string(b) != "product logo" {
		t.Fatalf("logo after registering, before any write: %d %q", c, b)
	}

	// Registered: the writes go through and show.
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"name": "Harbour Ops"}, nil); c != 200 {
		t.Fatalf("name: %d", c)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"name": "<b>x</b>"}, nil); c != 400 {
		t.Fatalf("markup in the name: %d", c)
	}
	if c, b := e.raw("PUT", "/api/v1/branding/logo", logo, "image/png"); c != 200 {
		t.Fatalf("logo: %d %s", c, b)
	}
	if c, _ := e.raw("PUT", "/api/v1/branding/logo", []byte(`<svg onload="alert(1)"/>`), "image/svg+xml"); c != 400 {
		t.Fatalf("svg: %d", c)
	}
	if c, _ := e.raw("PUT", "/api/v1/branding/icon", pngOf(t, 4, 4), "image/png"); c != 400 {
		t.Fatalf("tiny icon: %d", c)
	}
	if c, b := e.raw("GET", "/logo.png", nil, ""); c != 200 || !bytes.Equal(b, logo) {
		t.Fatalf("logo after registering: %d", c)
	}
	if c, b := e.raw("GET", "/api/v1/branding/logo/preview", nil, ""); c != 200 || !bytes.Equal(b, logo) {
		t.Fatalf("preview: %d", c)
	}
	_ = e.do("GET", "/api/v1/brand", nil, &pub)
	if pub.Name != "Harbour Ops" {
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

var packUI = fstest.MapFS{
	"index.html":            {Data: []byte("<html></html>")},
	"logo.png":              {Data: []byte("product logo")},
	"favicon.png":           {Data: []byte("product icon")},
	"wall.html":             {Data: []byte("<html lang=\"en\" data-theme=\"ink\">\n<head>\n<link rel=\"stylesheet\" href=\"/app.css\">\n</head>\n<body class=\"wallpage\"></body>\n</html>")},
	"styles/styles.json":    {Data: []byte(`[{"id": "modern"}, {"id": "datacenter", "registered": true}]`)},
	"styles/modern.css":     {Data: []byte(":root[data-style=modern] {}")},
	"styles/datacenter.css": {Data: []byte(":root[data-style=datacenter] {}")},
}

// The scenario styles are registered: an unregistered installation
// lists them as not allowed, cannot load their sheets and cannot set
// them as the default; a registered one can, and the wall shell then
// carries the style. Free styles are unaffected throughout.
func TestRegisteredStyles(t *testing.T) {
	e := newEnv(t, testBind, packUI)
	e.setup()

	var pub struct {
		Style  string
		Styles []struct {
			ID      string
			Allowed bool
		}
	}
	if c := e.do("GET", "/api/v1/brand", nil, &pub); c != 200 || len(pub.Styles) != 3 {
		t.Fatalf("public: %d %+v", c, pub)
	}
	allowed := map[string]bool{}
	for _, st := range pub.Styles {
		allowed[st.ID] = st.Allowed
	}
	if !allowed["classic"] || !allowed["modern"] || allowed["datacenter"] {
		t.Fatalf("allowed before registering: %v", allowed)
	}
	if c, _ := e.raw("GET", "/styles/datacenter.css", nil, ""); c != 404 {
		t.Fatalf("registered sheet unregistered: %d", c)
	}
	if c, _ := e.raw("GET", "/styles/modern.css", nil, ""); c != 200 {
		t.Fatalf("free sheet unregistered: %d", c)
	}
	var er struct{ Code string }
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "datacenter"}, &er); c != 402 || er.Code != CodeRegistration {
		t.Fatalf("registered style as default, unregistered: %d %+v", c, er)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "modern"}, nil); c != 200 {
		t.Fatalf("free style as default, unregistered: %d", c)
	}
	if c, b := e.raw("GET", "/wall", nil, ""); c != 200 || !strings.Contains(string(b), `data-style="modern"`) || !strings.Contains(string(b), "styles/modern.css") {
		t.Fatalf("wall shell with a free default: %d %s", c, b)
	}

	e.register()
	_ = e.do("GET", "/api/v1/brand", nil, &pub)
	allowed = map[string]bool{}
	for _, st := range pub.Styles {
		allowed[st.ID] = st.Allowed
	}
	if !allowed["datacenter"] {
		t.Fatalf("allowed after registering: %v", allowed)
	}
	if c, _ := e.raw("GET", "/styles/datacenter.css", nil, ""); c != 200 {
		t.Fatalf("registered sheet registered: %d", c)
	}
	if c := e.do("PUT", "/api/v1/branding", map[string]string{"style": "datacenter"}, nil); c != 200 {
		t.Fatalf("registered style as default, registered: %d", c)
	}
	if c, b := e.raw("GET", "/wall", nil, ""); c != 200 || !strings.Contains(string(b), `data-style="datacenter"`) || !strings.Contains(string(b), "styles/datacenter.css") {
		t.Fatalf("wall shell with a registered default: %d %s", c, b)
	}
}
