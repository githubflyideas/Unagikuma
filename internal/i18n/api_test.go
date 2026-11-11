package i18n

import (
	"regexp"
	"strings"
	"testing"
)

var verbRe = regexp.MustCompile(`%(\[\d+\])?[sdqv]`)

// Every translated API message takes the same arguments as the English one,
// and formats without errors.
func TestAPIMessagesKeepTheirArguments(t *testing.T) {
	langs := 0
	for _, l := range Langs() {
		if l.Code == "en" {
			continue
		}
		fs := APIFormats(l.Code)
		if len(fs) == 0 {
			t.Errorf("%s has no API messages", l.Code)
			continue
		}
		langs++
		for _, f := range fs {
			args := make([]any, len(verbRe.FindAllString(f, -1)))
			for i := range args {
				args[i] = "x"
				if strings.Contains(verbRe.FindAllString(f, -1)[i], "d") {
					args[i] = 3
				}
			}
			msg, ok := APIMessage(l.Code, f, args...)
			if !ok || strings.Contains(msg, "%!") {
				t.Errorf("%s: %q → %q", l.Code, f, msg)
			}
			if got, want := len(verbRe.FindAllString(apiCatalog()[l.Code][f], -1)), len(args); got != want {
				t.Errorf("%s: %q has %d arguments, the English %d", l.Code, f, got, want)
			}
		}
	}
	if langs != 14 {
		t.Errorf("%d languages translate API messages, want 14", langs)
	}
	if msg, ok := APIMessage("zh", "range is 7d, 30d or 90d"); !ok || msg != "range 只能是 7d、30d 或 90d" {
		t.Errorf("zh: %q %v", msg, ok)
	}
}
