package server

import (
	"archive/zip"
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
	"github.com/githubflyideas/unagikuma/internal/notify"
)

func testPack(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	img.Set(3, 3, color.RGBA{0, 0, 255, 255})
	var logo bytes.Buffer
	png.Encode(&logo, img)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, data := range map[string][]byte{
		"theme.json": []byte(`{"name":"Acme Ops","base":"dark","layout":"duty","brand":{"product":"Acme Monitor","hide_powered":true},"colors":{"accent":"#0a6cff"}}`),
		"logo.png":   logo.Bytes(),
	} {
		w, _ := zw.Create(name)
		w.Write(data)
	}
	zw.Close()
	return b.Bytes()
}

func (e *env) raw(method, path string, body []byte, ctype string) (int, []byte, http.Header) {
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
	return resp.StatusCode, data, resp.Header
}

// A theme pack's colours work for everyone; its product name, logo and
// "Powered by" switch need the branding feature of a registration.
func TestThemePackWhiteLabel(t *testing.T) {
	builtin := []byte("built-in logo")
	e := newEnv(t, testBind, fstest.MapFS{"index.html": {Data: []byte("<html>")}, "logo.png": {Data: builtin}, "favicon.png": {Data: builtin}})
	e.setup()

	var b brandJSON
	if code := e.do("GET", "/api/v1/brand", nil, &b); code != 200 || b.ID != "" {
		t.Fatalf("brand before any pack: %d %+v", code, b)
	}
	code, body, _ := e.raw("POST", "/api/v1/themes", testPack(t), "application/zip")
	if code != 200 || !strings.Contains(string(body), `"acme-ops"`) {
		t.Fatalf("upload: %d %s", code, body)
	}
	if code, body, _ := e.raw("POST", "/api/v1/themes", []byte("PK not a zip"), "application/zip"); code != 400 {
		t.Fatalf("bad upload: %d %s", code, body)
	}
	if code := e.do("PUT", "/api/v1/themes/active", map[string]string{"id": "../../etc"}, nil); code != 404 {
		t.Fatalf("activate a bad id: %d", code)
	}
	if code := e.do("PUT", "/api/v1/themes/active", map[string]string{"id": "acme-ops"}, nil); code != 200 {
		t.Fatalf("activate: %d", code)
	}

	// Unregistered: colours and layout, but not the white-label part.
	e.do("GET", "/api/v1/brand", nil, &b)
	if b.ID != "acme-ops" || b.Colors["accent"] != "#0a6cff" || b.Layout != "duty" || b.Base != "dark" || b.Product != "" || b.Logo || b.HidePowered || b.WhiteLabel {
		t.Fatalf("unregistered brand: %+v", b)
	}
	if _, got, _ := e.raw("GET", "/logo.png", nil, ""); !bytes.Equal(got, builtin) {
		t.Fatal("an unregistered installation served the pack's logo")
	}

	e.register(license.FeatBranding)
	e.do("GET", "/api/v1/brand", nil, &b)
	if b.Product != "Acme Monitor" || !b.Logo || !b.HidePowered || !b.WhiteLabel {
		t.Fatalf("registered brand: %+v", b)
	}
	code, got, h := e.raw("GET", "/logo.png", nil, "")
	if code != 200 || bytes.Equal(got, builtin) || h.Get("Content-Type") != "image/png" {
		t.Fatalf("logo: %d %s", code, h.Get("Content-Type"))
	}
	if _, got, _ := e.raw("GET", "/favicon.png", nil, ""); !bytes.Equal(got, builtin) {
		t.Fatal("a pack without a favicon replaced the built-in one")
	}
	if p, ok := e.app.Brand(); !ok || p.Brand.Product != "Acme Monitor" {
		t.Fatal("app brand")
	}
	title, _ := notify.Render(&notify.Message{Kind: "SYSTEM", Title: "x", Lang: "en", Brand: "Acme Monitor"})
	if !strings.Contains(title, "[Acme Monitor]") {
		t.Fatal(title)
	}

	// Export reads back; the pack in use cannot be deleted.
	code, zipped, _ := e.raw("GET", "/api/v1/themes/acme-ops/export", nil, "")
	if code != 200 || !bytes.HasPrefix(zipped, []byte("PK")) {
		t.Fatalf("export: %d", code)
	}
	if code := e.do("DELETE", "/api/v1/themes/acme-ops", nil, nil); code != 409 {
		t.Fatalf("deleted the pack in use: %d", code)
	}
	e.do("PUT", "/api/v1/themes/active", map[string]string{"id": ""}, nil)
	if code := e.do("DELETE", "/api/v1/themes/acme-ops", nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, got, _ := e.raw("GET", "/logo.png", nil, ""); !bytes.Equal(got, builtin) {
		t.Fatal("the built-in logo is not back")
	}
}

// Only administrators manage packs; anyone may read the brand.
func TestThemePackNeedsAdmin(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	e.csrf = ""
	e.c.Jar = nil
	if code := e.do("GET", "/api/v1/themes", nil, nil); code != 401 {
		t.Fatalf("list without a session: %d", code)
	}
	if code := e.do("GET", "/api/v1/brand", nil, nil); code != 200 {
		t.Fatalf("brand is public: %d", code)
	}
}

// /wall serves the wall screen page without leaking its URL fragment
// through the Referer header.
func TestWallPage(t *testing.T) {
	e := newEnv(t, testBind, fstest.MapFS{"index.html": {Data: []byte("<html>")}, "wall.html": {Data: []byte("<!doctype html>wall")}})
	code, body, h := e.raw("GET", "/wall", nil, "")
	if code != 200 || !strings.Contains(string(body), "wall") || h.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("/wall: %d %q %v", code, body, h)
	}
}

// The managers' summary: its page, and its API for a read-only token.
func TestSummary(t *testing.T) {
	e := newEnv(t, testBind, fstest.MapFS{"index.html": {Data: []byte("<html>")}, "summary.html": {Data: []byte("<!doctype html>summary")}})
	e.setup()
	if code, body, _ := e.raw("GET", "/summary", nil, ""); code != 200 || !strings.Contains(string(body), "summary") {
		t.Fatalf("/summary: %d %q", code, body)
	}
	var sum summaryJSON
	if code := e.do("GET", "/api/v1/reports/summary?range=7d", nil, &sum); code != 200 {
		t.Fatalf("summary: %d", code)
	}
	if sum.Range != "7d" || len(sum.Days) != 7 || sum.Now.Total == 0 || sum.Attention == nil || sum.Recent == nil {
		t.Fatalf("summary: %+v", sum)
	}
	// An unknown or too short range falls back to 30 days.
	e.do("GET", "/api/v1/reports/summary?range=1h", nil, &sum)
	if sum.Range != "30d" || len(sum.Days) != 30 {
		t.Fatalf("fallback range: %s %d", sum.Range, len(sum.Days))
	}
}
