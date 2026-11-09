package app

import (
	"github.com/githubflyideas/unagikuma/internal/branding"
	"github.com/githubflyideas/unagikuma/internal/license"
)

// Themes is the store of theme packs.
func (a *App) Themes() *branding.Store { return a.themes }

// Brand is the active theme pack (nil for the built-in look) and whether
// its white-label part (product name, logo, no "Powered by") may be used:
// that needs a registered installation; colours and layouts are free.
func (a *App) Brand() (*branding.Pack, bool) {
	p := a.themes.Active()
	if p == nil {
		return nil, false
	}
	return p, a.license.State().Allowed(license.FeatBranding)
}

// brandName is the product name messages carry, "" for the usual one.
func (a *App) brandName() string {
	if p, ok := a.Brand(); ok {
		return p.Brand.Product
	}
	return ""
}
