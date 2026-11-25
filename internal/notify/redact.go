package notify

import (
	"net/url"
	"regexp"
	"strings"
)

// Redact replaces every occurrence of the given secrets with "***".
func Redact(s string, secrets []string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// RedactURL keeps scheme and host: webhook URLs carry their credential in
// the path or query.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(url)"
	}
	s := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		s += "/…"
	}
	return s
}

var urlInText = regexp.MustCompile(`https?://[^\s"'<>]+`)
var botToken = regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`)

// scrub removes URLs and bot tokens from error texts and response
// snippets before they are stored or shown.
func scrub(s string, secrets []string) string {
	s = urlInText.ReplaceAllStringFunc(s, RedactURL)
	s = botToken.ReplaceAllString(s, "bot***")
	s = Redact(s, secrets)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
