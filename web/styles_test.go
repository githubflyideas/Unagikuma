package web

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// Every listed style has its file, scopes every rule to itself, and is
// named in every language.
func TestStyles(t *testing.T) {
	ui, err := FS()
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(ui, "styles/styles.json")
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		ID         string `json:"id"`
		Registered bool   `json:"registered"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	for _, st := range list {
		css, err := fs.ReadFile(ui, "styles/"+st.ID+".css")
		if err != nil {
			t.Errorf("%s: %v", st.ID, err)
			continue
		}
		scope := `[data-style="` + st.ID + `"]`
		for _, line := range strings.Split(string(css), "\n") {
			l := strings.TrimSpace(line)
			// A rule starts a line with a selector; it must name the style.
			if strings.HasSuffix(l, "{") && !strings.HasPrefix(l, "@") && !strings.Contains(l, scope) {
				t.Errorf("%s: rule not scoped to the style: %s", st.ID, l)
			}
		}
		for _, lang := range i18n.Langs() {
			cat, _ := i18n.Catalog(lang.Code)
			var m map[string]string
			_ = json.Unmarshal(cat, &m)
			if m["ui.style."+st.ID] == "" {
				t.Errorf("%s: no name in %s", st.ID, lang.Code)
			}
		}
	}
}
