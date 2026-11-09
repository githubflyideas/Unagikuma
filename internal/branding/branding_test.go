package branding

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	zw.Close()
	return b.Bytes()
}

const goodManifest = `{"name":"Acme Ops","author":"Acme","version":"1.0","base":"light","layout":"wall",
 "brand":{"product":"Acme Monitor","short":"AM","hide_powered":true},
 "colors":{"accent":"#0A6CFF","side":"#e6edf5"}}`

func TestParseAndStoreRoundTrip(t *testing.T) {
	data := zipOf(t, map[string][]byte{
		"acme/theme.json":  []byte(goodManifest),
		"acme/logo.png":    pngBytes(t, 64, 64),
		"acme/favicon.png": pngBytes(t, 32, 32),
		"__MACOSX/._x":     []byte("junk"),
	})
	p, files, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Acme Ops" || p.Colors["accent"] != "#0a6cff" || !p.HasLogo || !p.HasFavicon || p.Layout != "wall" || p.Brand.Product != "Acme Monitor" {
		t.Fatalf("pack: %+v", p)
	}
	s := NewStore(t.TempDir())
	saved, err := s.Add(p, files)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != "acme-ops" {
		t.Fatalf("id %q", saved.ID)
	}
	again, _ := s.Add(p, files)
	if again.ID != "acme-ops-2" {
		t.Fatalf("second id %q", again.ID)
	}
	if err := s.SetActive("acme-ops"); err != nil {
		t.Fatal(err)
	}
	if a := s.Active(); a == nil || a.ID != "acme-ops" {
		t.Fatalf("active %+v", a)
	}
	if err := s.Delete("acme-ops"); err == nil {
		t.Fatal("deleted the pack in use")
	}
	out, err := s.Export("acme-ops")
	if err != nil {
		t.Fatal(err)
	}
	p2, files2, err := Parse(out)
	if err != nil {
		t.Fatalf("exported pack does not read back: %v", err)
	}
	if p2.Name != p.Name || p2.Colors["side"] != "#e6edf5" || len(files2) != 2 || !bytes.Equal(files2["logo"], files["logo"]) {
		t.Fatalf("round trip: %+v", p2)
	}
	if len(s.List()) != 2 {
		t.Fatal("list")
	}
	s.SetActive("")
	if s.Active() != nil || s.Delete("acme-ops") != nil || len(s.List()) != 1 {
		t.Fatal("reset and delete")
	}
}

func TestParseRefuses(t *testing.T) {
	logo := pngBytes(t, 64, 64)
	cases := map[string]map[string][]byte{
		"no manifest":     {"logo.png": logo},
		"css":             {"theme.json": []byte(`{"name":"x"}`), "theme.css": []byte("body{}")},
		"svg logo":        {"theme.json": []byte(`{"name":"x"}`), "logo.svg": []byte("<svg onload=alert(1)>")},
		"fake png":        {"theme.json": []byte(`{"name":"x"}`), "logo.png": []byte("\x89PNG not really")},
		"tiny image":      {"theme.json": []byte(`{"name":"x"}`), "logo.png": pngBytes(t, 4, 4)},
		"unknown colour":  {"theme.json": []byte(`{"name":"x","colors":{"font":"#000000"}}`)},
		"css in colour":   {"theme.json": []byte(`{"name":"x","colors":{"bg":"url(http://x)"}}`)},
		"unknown field":   {"theme.json": []byte(`{"name":"x","css":"body{}"}`)},
		"bad base":        {"theme.json": []byte(`{"name":"x","base":"neon"}`)},
		"bad layout":      {"theme.json": []byte(`{"name":"x","layout":"magic"}`)},
		"no name":         {"theme.json": []byte(`{"colors":{}}`)},
		"long product":    {"theme.json": []byte(`{"name":"x","brand":{"product":"` + strings.Repeat("a", 41) + `"}}`)},
		"control in name": {"theme.json": []byte("{\"name\":\"x\\u0007\"}")},
		"path escape":     {"theme.json": []byte(`{"name":"x"}`), "../logo.png": logo},
	}
	for name, files := range cases {
		if _, _, err := Parse(zipOf(t, files)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := Parse([]byte("not a zip")); err == nil {
		t.Error("accepted a non-zip")
	}
	if _, _, err := Parse(make([]byte, MaxZip+1)); err == nil {
		t.Error("accepted an oversized upload")
	}
}

func TestStoreRejectsBadIDs(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, id := range []string{"../etc", "a/b", "", ".hidden", "UPPER"} {
		if _, err := s.Get(id); err == nil {
			t.Errorf("Get(%q) worked", id)
		}
		if err := s.SetActive(id); id != "" && err == nil {
			t.Errorf("SetActive(%q) worked", id)
		}
		if _, err := s.Image(id, "logo"); err == nil {
			t.Errorf("Image(%q) worked", id)
		}
	}
	if slug(strings.Repeat("abc ", 40)) == "" || len(slug(strings.Repeat("abc ", 40))) > 32 {
		t.Error("slug length")
	}
	if slug("某某监控") != "theme" {
		t.Error("non-ASCII names fall back to theme")
	}
}
