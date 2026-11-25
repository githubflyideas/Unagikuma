package i18n

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

var placeholderRe = regexp.MustCompile(`\{[a-z0-9_]+\}`)

func placeholders(s string) string {
	p := placeholderRe.FindAllString(s, -1)
	sort.Strings(p)
	return strings.Join(slicesCompact(p), ",")
}

func slicesCompact(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// pluralForms lists the CLDR categories (besides "other") a language
// distinguishes for integers, and those matching exactly one number
// (whose text may spell the number out instead of using {n}).
var pluralForms = map[string]struct{ forms, exact []string }{
	"en": {[]string{"one"}, []string{"one"}}, "de": {[]string{"one"}, []string{"one"}},
	"es": {[]string{"one"}, []string{"one"}}, "ur": {[]string{"one"}, []string{"one"}},
	"fr": {[]string{"one"}, nil}, "pt": {[]string{"one"}, nil}, "hi": {[]string{"one"}, nil}, "bn": {[]string{"one"}, nil},
	"ru": {[]string{"one", "few", "many"}, nil},
	"ar": {[]string{"zero", "one", "two", "few", "many"}, []string{"zero", "one", "two"}},
	"zh": {}, "ja": {}, "ko": {}, "vi": {}, "id": {},
}

func splitPlural(key string) (base, form string) {
	if i := strings.LastIndexByte(key, '_'); i > 0 {
		switch f := key[i+1:]; f {
		case "zero", "one", "two", "few", "many":
			return key[:i], f
		}
	}
	return key, ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestCatalogsComplete checks every shipped language against English:
// all keys present, the same placeholders, and only plural variants the
// language actually uses.
func TestCatalogsComplete(t *testing.T) {
	en := catalogs["en"]
	for _, l := range langs {
		c, ok := catalogs[l.Code]
		if !ok {
			t.Errorf("%s: catalog missing", l.Code)
			continue
		}
		pf, ok := pluralForms[l.Code]
		if !ok {
			t.Errorf("%s: plural forms not described", l.Code)
		}
		for k, v := range en {
			if _, form := splitPlural(k); form != "" {
				continue
			}
			tr, ok := c[k]
			if !ok {
				t.Errorf("%s: missing %s", l.Code, k)
				continue
			}
			if strings.TrimSpace(tr) == "" && strings.TrimSpace(v) != "" {
				t.Errorf("%s: empty %s", l.Code, k)
			}
			if placeholders(tr) != placeholders(v) {
				t.Errorf("%s: %s placeholders %q, English has %q", l.Code, k, placeholders(tr), placeholders(v))
			}
		}
		for k, tr := range c {
			base, form := splitPlural(k)
			ev, ok := en[base]
			if !ok {
				t.Errorf("%s: %s is not an English key", l.Code, k)
				continue
			}
			if form == "" {
				continue
			}
			if !contains(pf.forms, form) {
				t.Errorf("%s: %s uses plural form %q the language does not have", l.Code, k, form)
				continue
			}
			want := placeholders(ev)
			got := placeholders(tr)
			if contains(pf.exact, form) {
				// The number may be spelled out; other placeholders must stay.
				want = placeholders(strings.ReplaceAll(ev, "{n}", ""))
				got = placeholders(strings.ReplaceAll(tr, "{n}", ""))
			}
			if got != want {
				t.Errorf("%s: %s placeholders %q, want %q", l.Code, k, got, want)
			}
		}
	}
}
