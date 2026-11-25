package app

import "testing"

// A line whose names are all empty is left out; other lines stay.
func TestRenderLines(t *testing.T) {
	data := map[string]any{"Impact": "", "Title": "x", "Note": "n"}
	got, err := renderLines("Change: {{.Title}}\nImpact: {{.Impact}}\n{{.Note}}\nplain line", data)
	if err != nil || got != "Change: x\nn\nplain line" {
		t.Fatalf("%q %v", got, err)
	}
	got, _ = renderLines("{{if .Impact}}Impact: {{.Impact}}{{else}}none{{end}}", data)
	if got != "none" {
		t.Fatalf("blocks: %q", got)
	}
	for _, tpls := range defaultChangeTemplates {
		for ev, tpl := range tpls {
			if _, _, err := renderChangeTemplate(tpl, map[string]any{"Title": "t", "Start": "s", "End": "e", "Duration": "1h", "By": "a", "Now": "n", "In": "1h"}); err != nil {
				t.Fatalf("%s: %v", ev, err)
			}
		}
	}
}
