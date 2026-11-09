// Package i18n holds unagikuma's translations: one flat JSON catalog per
// language with {placeholder} parameters and CLDR plural suffixes
// (key_one, key_few, ...). The same catalogs serve the Web UI and the
// texts the server writes itself: notifications, system messages and
// reason descriptions.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed locales/*.json
var files embed.FS

// Lang describes a supported language.
type Lang struct {
	Code string `json:"code"`
	Name string `json:"name"`
	RTL  bool   `json:"rtl,omitempty"`
}

// langs is ordered by total number of speakers.
var langs = []Lang{
	{"en", "English", false}, {"zh", "中文", false}, {"hi", "हिन्दी", false}, {"es", "Español", false},
	{"ar", "العربية", true}, {"fr", "Français", false}, {"bn", "বাংলা", false}, {"pt", "Português", false},
	{"ru", "Русский", false}, {"id", "Bahasa Indonesia", false}, {"ur", "اردو", true}, {"de", "Deutsch", false},
	{"ja", "日本語", false}, {"vi", "Tiếng Việt", false}, {"ko", "한국어", false},
}

var catalogs = map[string]map[string]string{}

func init() {
	for _, l := range langs {
		b, err := files.ReadFile("locales/" + l.Code + ".json")
		if err != nil {
			continue // a language not shipped yet falls back to English
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			panic(fmt.Sprintf("i18n: locales/%s.json: %v", l.Code, err))
		}
		catalogs[l.Code] = m
	}
	if catalogs["en"] == nil {
		panic("i18n: the English catalog is missing")
	}
}

// Langs returns the supported languages.
func Langs() []Lang { return append([]Lang(nil), langs...) }

// Normalize maps a language tag ("zh-CN", "pt_BR", "EN") to a supported
// code, or "en".
func Normalize(tag string) string {
	c := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	for _, l := range langs {
		if l.Code == c {
			return c
		}
	}
	return "en"
}

// Catalog returns a language's catalog for the UI as JSON, with English
// filling any key the language lacks. Unknown languages report false.
func Catalog(code string) ([]byte, bool) {
	c := strings.ToLower(code)
	if Normalize(c) != c && c != "en" {
		return nil, false
	}
	m := make(map[string]string, len(catalogs["en"]))
	for k, v := range catalogs["en"] {
		m[k] = v
	}
	for k, v := range catalogs[c] {
		m[k] = v
	}
	m["_details"] = string(detailsJSON)
	b, err := json.Marshal(m)
	return b, err == nil
}

// Keys returns the English keys (for tests and tooling).
func Keys() []string {
	out := make([]string, 0, len(catalogs["en"]))
	for k := range catalogs["en"] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lookup(lang, key string) (string, bool) {
	if s, ok := catalogs[Normalize(lang)][key]; ok {
		return s, true
	}
	s, ok := catalogs["en"][key]
	return s, ok
}

// T translates key, substituting {name} placeholders from args given as
// name, value pairs. Unknown keys render as the key itself.
func T(lang, key string, args ...any) string {
	s, ok := lookup(lang, key)
	if !ok {
		s = key
	}
	return fill(s, args)
}

// N translates a count-dependent key: it uses key_<form> when the language
// defines that plural form for n, else key. {n} is set to n.
func N(lang, key string, n int, args ...any) string {
	lang = Normalize(lang)
	args = append(args, "n", n)
	if f := pluralForm(lang, n); f != "other" {
		if s, ok := catalogs[lang][key+"_"+f]; ok {
			return fill(s, args)
		}
	}
	return T(lang, key, args...)
}

func fill(s string, args []any) string {
	if len(args) < 2 || !strings.Contains(s, "{") {
		return s
	}
	pairs := make([]string, 0, len(args))
	for i := 0; i+1 < len(args); i += 2 {
		pairs = append(pairs, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// pluralForm implements the CLDR cardinal rules of the supported
// languages for integers.
func pluralForm(lang string, n int) string {
	if n < 0 {
		n = -n
	}
	switch lang {
	case "zh", "ja", "ko", "vi", "id":
		return "other"
	case "fr", "pt", "hi", "bn":
		if n == 0 || n == 1 {
			return "one"
		}
	case "ru":
		switch m10, m100 := n%10, n%100; {
		case m10 == 1 && m100 != 11:
			return "one"
		case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
			return "few"
		default:
			return "many"
		}
	case "ar":
		switch m100 := n % 100; {
		case n == 0:
			return "zero"
		case n == 1:
			return "one"
		case n == 2:
			return "two"
		case m100 >= 3 && m100 <= 10:
			return "few"
		case m100 >= 11:
			return "many"
		}
	default: // en de es ur
		if n == 1 {
			return "one"
		}
	}
	return "other"
}

// Duration renders a duration compactly in a language ("2h5m", "2小时5分",
// "2 д 5 мин").
func Duration(d time.Duration, lang string) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return T(lang, "dur.s", "n", int(d.Round(time.Second).Seconds()))
	}
	d = d.Round(time.Minute)
	units := []struct {
		n   int
		key string
	}{{int(d / (24 * time.Hour)), "dur.d"}, {int(d % (24 * time.Hour) / time.Hour), "dur.h"}, {int(d % time.Hour / time.Minute), "dur.m"}}
	var parts []string
	for _, u := range units {
		if u.n > 0 {
			parts = append(parts, T(lang, u.key, "n", u.n))
		}
	}
	sep := " "
	switch Normalize(lang) {
	case "en", "zh", "ja", "ko":
		sep = ""
	}
	return strings.Join(parts, sep)
}
