package i18n

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"sync"
)

// Check results and configuration warnings are written in English where
// they happen. Detail translates the common ones: details.json lists
// patterns, each with a catalog key "detail.<key>" whose {name}
// placeholders are filled from the pattern's named groups (a group named
// "inner" is translated itself). Anything else stays as it is. The UI
// uses the same table (shipped with each catalog as "_details").
//
//go:embed details.json
var detailsJSON []byte

type detailPattern struct {
	Key string `json:"key"`
	RE  string `json:"re"`
	re  *regexp.Regexp
}

var (
	detailOnce sync.Once
	details    []detailPattern
)

func loadDetails() {
	if err := json.Unmarshal(detailsJSON, &details); err != nil {
		panic("i18n: details.json: " + err.Error())
	}
	for i := range details {
		details[i].re = regexp.MustCompile(details[i].RE)
	}
}

// Detail translates a check result detail or a warning into lang; text it
// does not know is returned unchanged.
func Detail(lang, s string) string {
	detailOnce.Do(loadDetails)
	if s == "" || Normalize(lang) == "en" {
		return s
	}
	return detail(lang, s, 0)
}

func detail(lang, s string, depth int) string {
	for _, p := range details {
		m := p.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		key := "detail." + p.Key
		if _, ok := catalogs[Normalize(lang)][key]; !ok {
			return s
		}
		var args []any
		for i, name := range p.re.SubexpNames() {
			if name == "" {
				continue
			}
			v := m[i]
			if name == "inner" && depth < 2 {
				v = detail(lang, v, depth+1)
			}
			args = append(args, name, v)
		}
		return T(lang, key, args...)
	}
	return s
}
