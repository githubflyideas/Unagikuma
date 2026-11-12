package notify

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// limit is the longest text a service accepts, in the unit it counts
// (0 = not limited in that unit).
type limit struct{ runes, bytes, utf16 int }

// textLimits are the documented message limits of the chat services, with
// some room left where the limit is on the whole request.
var textLimits = map[string]limit{
	"wecom":      {bytes: 2048},  // text message content
	"telegram":   {utf16: 4096},  // sendMessage text
	"discord":    {runes: 2000},  // webhook content
	"dingtalk":   {bytes: 18000}, // 20000 bytes for the whole message
	"feishu":     {bytes: 18000}, // 20 KB for the whole request
	"slack":      {runes: 39000}, // 40000 characters of text
	"twilio-sms": {runes: 1600},  // concatenated message body
}

func (l limit) fits(s string) bool {
	if l.bytes > 0 && len(s) > l.bytes {
		return false
	}
	if l.runes > 0 && utf8.RuneCountInString(s) > l.runes {
		return false
	}
	if l.utf16 > 0 && len(utf16.Encode([]rune(s))) > l.utf16 {
		return false
	}
	return true
}

// fit shortens s to the limit. Whole lines are dropped from the end and a
// last line says how many, so a long report still arrives and says that
// it is not complete; a single line that is too long is cut.
func fit(s string, l limit, lang string) string {
	if l == (limit{}) || l.fits(s) {
		return s
	}
	lines := strings.Split(s, "\n")
	for n := len(lines) - 1; n >= 1; n-- {
		c := strings.Join(lines[:n], "\n") + "\n" + i18n.T(lang, "msg.cut", "n", len(lines)-n)
		if l.fits(c) {
			return c
		}
	}
	return cut(s, l)
}

// cut keeps the longest beginning of s that fits, ending in "…".
func cut(s string, l limit) string {
	if l == (limit{}) || l.fits(s) {
		return s
	}
	r := []rune(s)
	lo, hi := 0, len(r)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if l.fits(string(r[:mid]) + "…") {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(r[:lo]) + "…"
}
