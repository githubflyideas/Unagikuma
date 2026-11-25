package i18n

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every counted message has each plural form its language's grammar uses
// (by the rules pluralForm implements), so a count of 1, 2 or 5 never falls
// back to another form's wording.
func TestEveryLanguageHasItsPluralForms(t *testing.T) {
	read := func(lang string) map[string]string {
		b, err := os.ReadFile(filepath.Join("locales", lang+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	en := read("en")
	bases := map[string]bool{}
	for k := range en {
		if i := strings.LastIndex(k, "_"); i > 0 && k[i+1:] == "one" {
			bases[k[:i]] = true
		}
	}
	files, _ := filepath.Glob(filepath.Join("locales", "*.json"))
	for _, f := range files {
		lang := strings.TrimSuffix(filepath.Base(f), ".json")
		need := map[string]bool{}
		for n := 0; n <= 200; n++ {
			if form := pluralForm(lang, n); form != "other" {
				need[form] = true
			}
		}
		m := read(lang)
		for b := range bases {
			for form := range need {
				if _, ok := m[b+"_"+form]; !ok {
					t.Errorf("%s: %s_%s is missing", lang, b, form)
				}
			}
		}
	}
}
