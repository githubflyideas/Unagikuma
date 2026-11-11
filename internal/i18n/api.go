package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The API's error messages are written in English in the code; api/<lang>.json
// maps each message format to the language's own, with the same verbs (Go
// fmt, %[n]s where the order changes).

//go:embed api/*.json
var apiFiles embed.FS

var (
	apiOnce sync.Once
	apiCat  map[string]map[string]string
)

func apiCatalog() map[string]map[string]string {
	apiOnce.Do(func() {
		apiCat = map[string]map[string]string{}
		entries, _ := apiFiles.ReadDir("api")
		for _, e := range entries {
			b, err := apiFiles.ReadFile("api/" + e.Name())
			if err != nil {
				continue
			}
			m := map[string]string{}
			if json.Unmarshal(b, &m) == nil {
				apiCat[strings.TrimSuffix(e.Name(), ".json")] = m
			}
		}
	})
	return apiCat
}

// APIMessage formats an API error message in lang; ok is false when the
// language has no translation for format.
func APIMessage(lang, format string, args ...any) (string, bool) {
	tr, ok := apiCatalog()[lang][format]
	if !ok || tr == "" {
		return "", false
	}
	return fmt.Sprintf(tr, args...), true
}

// APIFormats lists the message formats lang translates (for tests).
func APIFormats(lang string) []string {
	var out []string
	for f := range apiCatalog()[lang] {
		out = append(out, f)
	}
	return out
}
