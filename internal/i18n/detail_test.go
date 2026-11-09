package i18n

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDetailTranslated(t *testing.T) {
	cases := []struct{ lang, in, want string }{
		{"zh", "HTTP 500, expected 200-399", "HTTP 500，期望 200-399"},
		{"ja", "connection refused", "接続が拒否されました"},
		{"zh", "HTTP 200; certificate expires in 12 days", "HTTP 200；证书将在 12 天后过期"},
		{"en", "connection refused", "connection refused"},
		{"zh", "something nobody knows", "something nobody knows"},
		{"de", "keyword \"Welcome\" not found in the response", "Schlüsselwort \"Welcome\" nicht in der Antwort"},
	}
	for _, c := range cases {
		if got := Detail(c.lang, c.in); got != c.want {
			t.Errorf("%s %q: got %q want %q", c.lang, c.in, got, c.want)
		}
	}
}

// Every pattern has its key in every catalog, the placeholders match the
// pattern's groups, and the UI gets the table too.
func TestDetailPatternsComplete(t *testing.T) {
	detailOnce.Do(loadDetails)
	for _, p := range details {
		key := "detail." + p.Key
		groups := map[string]bool{}
		for _, n := range p.re.SubexpNames() {
			if n != "" {
				groups[n] = true
			}
		}
		for _, l := range langs {
			s, ok := catalogs[l.Code][key]
			if !ok {
				t.Errorf("%s: %s missing", l.Code, key)
				continue
			}
			for _, part := range strings.Split(s, "{")[1:] {
				name, _, _ := strings.Cut(part, "}")
				if !groups[name] && !strings.HasPrefix(name, "secret:") {
					t.Errorf("%s %s: placeholder {%s} not in the pattern", l.Code, key, name)
				}
			}
		}
	}
	b, _ := Catalog("ja")
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	if !strings.Contains(m["_details"], "http_status") {
		t.Fatal("catalog lacks the detail patterns")
	}
}
