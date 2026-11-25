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

// The messages of the value rules reach reports and alerts in the
// reader's language, also when one is wrapped in another.
func TestValueRuleDetails(t *testing.T) {
	for in, want := range map[string]string{
		"291, limit >280":                                       "291，限值 >280",
		"291, limit >280; for 2m0s of 5m0s":                     "291，限值 >280；已持续 2m0s（需要 5m0s）",
		"the check reported no value for these rules":           "检查没有给出这些规则所需的数值",
		"12; learning, a reference in 3h0m0s":                   "12；学习中，还需 3h0m0s 才有参考值",
		"this is not Redis INFO: no field used_memory":          "这不是 Redis INFO 的回复",
		"something nobody has translated, limit stays as it is": "something nobody has translated, limit stays as it is",
	} {
		if got := Detail("zh", in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
