package i18n

import (
	"testing"
	"time"
)

func TestTAndFallback(t *testing.T) {
	if got := T("zh", "status.ok"); got != "正常" {
		t.Fatalf("zh: %q", got)
	}
	if got := T("zh-CN", "msg.count", "count", 2, "max", 3); got != "第 2/3 次通知" {
		t.Fatalf("params: %q", got)
	}
	if got := T("xx", "status.ok"); got != "OK" {
		t.Fatalf("unknown language falls back to English: %q", got)
	}
	if got := T("en", "no.such.key"); got != "no.such.key" {
		t.Fatalf("unknown key: %q", got)
	}
}

func TestPlural(t *testing.T) {
	if got := N("en", "sys.digest_title", 1); got != "1 more alert merged" {
		t.Fatalf("%q", got)
	}
	if got := N("en", "sys.digest_title", 5); got != "5 more alerts merged" {
		t.Fatalf("%q", got)
	}
	if got := N("zh", "sys.digest_title", 1); got != "另有 1 条告警已合并" {
		t.Fatalf("%q", got)
	}
	cases := map[string]map[int]string{
		"ru": {1: "one", 2: "few", 5: "many", 11: "many", 21: "one", 22: "few"},
		"ar": {0: "zero", 1: "one", 2: "two", 3: "few", 11: "many", 100: "other"},
		"fr": {0: "one", 1: "one", 2: "other"},
		"ja": {1: "other"},
		"en": {0: "other", 1: "one"},
	}
	for lang, m := range cases {
		for n, want := range m {
			if got := pluralForm(lang, n); got != want {
				t.Errorf("%s %d: %s want %s", lang, n, got, want)
			}
		}
	}
}

func TestNormalizeAndLangs(t *testing.T) {
	if Normalize("pt_BR") != "pt" || Normalize("ZH-hant") != "zh" || Normalize("") != "en" || Normalize("tlh") != "en" {
		t.Fatal("normalize")
	}
	if len(Langs()) != 15 || !Langs()[4].RTL {
		t.Fatal("langs")
	}
	if b, ok := Catalog("zh"); !ok || len(b) == 0 {
		t.Fatal("catalog")
	}
}

func TestDuration(t *testing.T) {
	cases := map[string]string{"en": "1d2h5m", "zh": "1天2小时5分"}
	for lang, want := range cases {
		if got := Duration(26*time.Hour+5*time.Minute, lang); got != want {
			t.Errorf("%s: %q", lang, got)
		}
	}
	if Duration(30*time.Second, "en") != "30s" || Duration(-90*time.Second, "en") != "2m" {
		t.Error("short durations")
	}
}
