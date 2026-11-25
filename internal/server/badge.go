package server

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"
)

// Badge colours follow the status palette; text stays readable on all.
var badgeColors = map[string]string{
	"ok":          "#2e8540",
	"degraded":    "#b7791f",
	"partial":     "#b7791f",
	"critical":    "#c53030",
	"unknown":     "#6b7280",
	"pending":     "#6b7280",
	"paused":      "#6b7280",
	"maintenance": "#2b6cb0",
}

// BadgeColor returns the colour for a status (grey when unknown).
func BadgeColor(status string) string {
	if c, ok := badgeColors[status]; ok {
		return c
	}
	return badgeColors["unknown"]
}

// textWidth estimates rendered width in px for 11px Verdana-like text:
// wide CJK characters count double.
func textWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x2E80:
			w += 11
		case strings.ContainsRune("mwMW@%", r):
			w += 9
		case strings.ContainsRune("ijl.,:;|!'1 ", r):
			w += 4
		default:
			w += 7
		}
	}
	return w
}

// RenderBadge renders a flat two-part badge ("label | value") as SVG.
// Label and value are escaped; length is capped to keep badges sane.
func RenderBadge(label, value, color string) []byte {
	label, value = clip(label, 40), clip(value, 40)
	lw, vw := textWidth(label)+10, textWidth(value)+10
	total := lw + vw
	l, v := html.EscapeString(label), html.EscapeString(value)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`, total, l, v)
	fmt.Fprintf(&b, `<title>%s: %s</title>`, l, v)
	fmt.Fprintf(&b, `<clipPath id="r"><rect width="%d" height="20" rx="3"/></clipPath><g clip-path="url(#r)">`, total)
	fmt.Fprintf(&b, `<rect width="%d" height="20" fill="#555"/><rect x="%d" width="%d" height="20" fill="%s"/></g>`, lw, lw, vw, html.EscapeString(color))
	fmt.Fprintf(&b, `<g fill="#fff" text-anchor="middle" font-family="Verdana,DejaVu Sans,sans-serif" font-size="11">`)
	fmt.Fprintf(&b, `<text x="%d" y="14">%s</text><text x="%d" y="14">%s</text></g></svg>`, lw/2, l, lw+vw/2, v)
	return []byte(b.String())
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
