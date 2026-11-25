package server

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestRenderBadge(t *testing.T) {
	svg := RenderBadge("官网", "99.95%", BadgeColor("ok"))
	if err := xml.Unmarshal(svg, new(struct{})); err != nil {
		t.Fatalf("invalid SVG: %v\n%s", err, svg)
	}
	if !strings.Contains(string(svg), "#2e8540") || !strings.Contains(string(svg), "99.95%") {
		t.Fatalf("%s", svg)
	}
	evil := RenderBadge(`<script>alert(1)</script>`, `"><x`, `red"/><script>`)
	if strings.Contains(string(evil), "<script>") || strings.Contains(string(evil), `"><x`) {
		t.Fatalf("not escaped: %s", evil)
	}
	if err := xml.Unmarshal(evil, new(struct{})); err != nil {
		t.Fatalf("escaped badge must stay valid XML: %v", err)
	}
	long := RenderBadge(strings.Repeat("a", 200), "x", "#000")
	if strings.Contains(string(long), strings.Repeat("a", 41)) || !strings.Contains(string(long), strings.Repeat("a", 39)+"…") {
		t.Fatal("label not clipped")
	}
	if BadgeColor("weird") != BadgeColor("unknown") {
		t.Fatal("fallback colour")
	}
	if textWidth("中文") <= textWidth("ab") {
		t.Fatal("CJK width")
	}
}
