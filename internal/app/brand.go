package app

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif" // logos may be GIF
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"unicode/utf8"
)

// Branding puts an installation's own name, logo and browser icon in
// place of the product's. It can be filled in at any time, but only a
// registered installation shows it; the values are kept in the settings
// table, so they go with backups.

// ProductName is the name shown when no other is set.
const ProductName = "Unagikuma"

const (
	brandNameKey = "brand.name"
	brandLogoKey = "brand.logo"
	brandIconKey = "brand.icon"
)

// BrandImage names one of the two images.
type BrandImage string

// The images a brand has.
const (
	BrandLogo BrandImage = "logo"
	BrandIcon BrandImage = "icon"
)

// Sizes stay small: the images are sent with every page.
var brandImageMax = map[BrandImage]int{BrandLogo: 512 << 10, BrandIcon: 128 << 10}

func (k BrandImage) key() string {
	if k == BrandIcon {
		return brandIconKey
	}
	return brandLogoKey
}

// Brand is what has been set, whether or not it is in use.
type Brand struct {
	Name    string `json:"name"`
	Logo    bool   `json:"logo"`
	Icon    bool   `json:"icon"`
	Allowed bool   `json:"allowed"` // registered: the brand is in use
}

// BrandSettings reports the stored brand.
func (a *App) BrandSettings() Brand {
	name, _ := a.store.Setting(brandNameKey)
	logo, _ := a.store.Setting(brandLogoKey)
	icon, _ := a.store.Setting(brandIconKey)
	return Brand{Name: name, Logo: logo != "", Icon: icon != "", Allowed: a.brandAllowed()}
}

func (a *App) brandAllowed() bool {
	return a.license != nil && a.license.State().Registered
}

// BrandName is the name in use: the installation's own when it is
// registered and has one, the product's otherwise.
func (a *App) BrandName() string {
	if !a.brandAllowed() {
		return ProductName
	}
	if v, _ := a.store.Setting(brandNameKey); v != "" {
		return v
	}
	return ProductName
}

// ErrBrand is a brand value that cannot be used.
var ErrBrand = errors.New("brand")

// SetBrandName stores the name ("" for the product's own).
func (a *App) SetBrandName(name string) error {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 40 || strings.ContainsAny(name, "\r\n<>") {
		return errors.Join(ErrBrand, errors.New("the name is one line of at most 40 characters, without < or >"))
	}
	if name == "" {
		return a.store.DeleteSetting(brandNameKey)
	}
	return a.store.SetSetting(brandNameKey, name)
}

// SetBrandImage stores a PNG, JPEG or GIF image; nil removes it. Other
// formats, SVG among them, are refused: a picture shown on public pages
// must not carry scripts.
func (a *App) SetBrandImage(k BrandImage, b []byte) error {
	if b == nil {
		return a.store.DeleteSetting(k.key())
	}
	if len(b) > brandImageMax[k] {
		return errors.Join(ErrBrand, errors.New("the image is too large"))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || (format != "png" && format != "jpeg" && format != "gif") {
		return errors.Join(ErrBrand, errors.New("the image must be a PNG, JPEG or GIF file"))
	}
	if cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 2048 || cfg.Height > 2048 {
		return errors.Join(ErrBrand, errors.New("the image must be between 16 and 2048 pixels wide and high"))
	}
	return a.store.SetSetting(k.key(), base64.StdEncoding.EncodeToString(b))
}

// BrandImageData returns the image in use, if the installation is
// registered and has set one.
func (a *App) BrandImageData(k BrandImage) ([]byte, bool) {
	if !a.brandAllowed() {
		return nil, false
	}
	return a.StoredBrandImage(k)
}

// StoredBrandImage returns the stored image whether or not it is in use,
// for the preview on the settings page.
func (a *App) StoredBrandImage(k BrandImage) ([]byte, bool) {
	v, _ := a.store.Setting(k.key())
	if v == "" {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(v)
	return b, err == nil
}
