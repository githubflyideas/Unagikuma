package notify

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// monitorKinds are the messages about one monitor. template= rewrites
// these; summaries, reports and system notices keep their own text.
var monitorKinds = map[string]bool{"PROBLEM": true, "REPEAT": true, "REOPEN": true, "ESCALATE": true, "RECOVERY": true}

// messageData exposes the message (capitalised names) and the channel's
// options (lower case, dashes kept: use index for those) to templates.
// Title and Body are the built-in text, so a template can add to it.
func messageData(m *Message, o map[string]string) map[string]any {
	title, body := Render(m)
	reason := ""
	if m.Reason != "" {
		reason = i18n.T(m.Lang, "reason."+m.Reason)
	}
	started := ""
	if !m.Time.IsZero() {
		started = m.Time.Add(-m.Duration).Format("2006-01-02 15:04:05")
	}
	d := map[string]any{
		"Title": title, "Body": body, "Text": strings.TrimRight(title+"\n"+body, "\n"), "Kind": m.Kind, "Priority": m.Priority,
		"Monitor": m.Monitor, "Name": m.Title, "Type": m.Type, "Target": m.Target, "Depth": m.Depth,
		"Status": m.Status, "Reason": m.Reason, "Detail": sanitize(m.Detail, detailCap), "Quorum": m.Quorum,
		"Count": m.Count, "Max": m.Max, "IncidentID": m.IncidentID, "GlobalIncidentID": m.GlobalIncidentID, "Duration": m.Duration, "DetailURL": m.DetailURL,
		"AckURL": firstOf(m.AckURL, m.DetailURL), "Tags": m.Tags, "Time": m.Time, "Delayed": m.Delayed, "DryRun": m.DryRun,
		"Instance": m.Instance, "Site": m.Site, "DeliveryID": m.DeliveryID, "Class": m.Class, "Lang": m.Lang,
		// The same values in words, in the message language.
		"StatusText": statusText(m), "KindText": i18n.T(m.Lang, "msg.kind."+m.Kind), "ReasonText": reason,
		"DurationText": i18n.Duration(m.Duration, m.Lang), "Started": started, "Icon": kindIcon(m),
	}
	for k, v := range o {
		if _, taken := d[k]; !taken {
			d[k] = v
		}
	}
	return d
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// templateData is messageData with the channel's own text (template=)
// as Title, Body and Text. With escapeJSON, string values are escaped for
// use inside JSON string literals; {{raw .X}} undoes it.
func templateData(m *Message, o map[string]string, escapeJSON bool) map[string]any {
	d := messageData(m, o)
	title, body := textOf(o, m)
	d["Title"], d["Body"], d["Text"] = title, body, strings.TrimRight(title+"\n"+body, "\n")
	if escapeJSON {
		escapeForJSON(d)
	}
	return d
}

// escapeForJSON escapes every string for use inside a JSON string literal.
func escapeForJSON(d map[string]any) {
	for k, v := range d {
		if s, ok := v.(string); ok {
			b, _ := json.Marshal(s)
			d[k] = string(b[1 : len(b)-1])
		}
	}
}

// textOf is the title and body a channel sends for m: the built-in text,
// or what the channel's template= makes of a message about a monitor.
// The first line of a template's result is the title. A template that
// fails or renders nothing never costs the alert: the built-in text goes
// out instead.
func textOf(o map[string]string, m *Message) (title, body string) {
	title, body = channelText(o, m)
	// prefix= puts a fixed mark such as "[Beijing host1]" in front, so a
	// text message says which site sent it.
	if p := strings.TrimSpace(o["prefix"]); p != "" {
		title = p + " " + title
	}
	return title, body
}

func channelText(o map[string]string, m *Message) (title, body string) {
	title, body = Render(m)
	tpl := templateLines(o["template"])
	if tpl == "" {
		return title, body
	}
	switch {
	case monitorKinds[m.Kind]:
		if out, err := renderTemplate(tpl, messageData(m, o)); err == nil && strings.TrimSpace(out) != "" {
			title, body, _ = strings.Cut(strings.TrimSpace(out), "\n")
			return sanitize(title, 300), sanitize(body, 8000)
		}
	case m.Kind == "TEST":
		// A test shows what an alert through this channel will look like.
		if out, err := renderTemplate(tpl, messageData(sampleMessage(m), o)); err == nil && strings.TrimSpace(out) != "" {
			return title, sanitize(strings.TrimSpace(out), 8000)
		}
	}
	return title, body
}

// templateLines turns the two characters \n into a line break, outside
// {{ }}: a configuration value is one line, a message has several.
func templateLines(src string) string {
	if !strings.Contains(src, `\n`) {
		return src
	}
	var b strings.Builder
	inAction := false
	for i := 0; i < len(src); i++ {
		switch {
		case strings.HasPrefix(src[i:], "{{"):
			inAction = true
		case strings.HasPrefix(src[i:], "}}"):
			inAction = false
		case !inAction && strings.HasPrefix(src[i:], `\n`):
			b.WriteByte('\n')
			i++
			continue
		}
		b.WriteByte(src[i])
	}
	return b.String()
}

// sampleMessage is a made-up problem used to try templates: when a
// channel is saved and when it is tested.
func sampleMessage(m *Message) *Message {
	s := Message{Kind: "PROBLEM", Priority: "high", Monitor: "example", Title: "Example service", Type: "HTTP",
		Target: "https://example.com/health", Depth: "L3", Status: "critical", Reason: "http_status", Detail: "HTTP 503, expected 200-399",
		Count: 1, Max: 3, IncidentID: 1, GlobalIncidentID: "example", Duration: 90 * time.Second, DetailURL: "https://monitor.example.com/#/monitors/example",
		Tags: []string{"env:prod"}, Time: time.Now(), Instance: "unagikuma", DeliveryID: "0123456789abcdef", Lang: "en", Class: "business"}
	if m != nil {
		s.Lang, s.Instance, s.Site, s.Brand, s.Time = firstOf(m.Lang, "en"), firstOf(m.Instance, s.Instance), m.Site, m.Brand, m.Time
		if s.Time.IsZero() {
			s.Time = time.Now()
		}
	}
	return &s
}

var funcs = template.FuncMap{
	"raw": func(s any) string {
		str := fmt.Sprint(s)
		if u, err := strconv.Unquote(`"` + str + `"`); err == nil {
			return u
		}
		return str
	},
	"json": func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	},
	"urlquery":   url.QueryEscape,
	"pathescape": url.PathEscape,
	"trunc": func(n int, s string) string {
		if r := []rune(s); len(r) > n {
			return string(r[:n]) + "…"
		}
		return s
	},
	"seconds": func(d time.Duration) int { return int(d.Seconds()) },
	"rfc3339": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"upper":   strings.ToUpper,
	"lower":   strings.ToLower,
	"join":    func(sep string, list []string) string { return strings.Join(list, sep) },
	// split cuts a list such as "a, b" into its items, without the blanks.
	"split": func(sep, s string) []string {
		var out []string
		for _, x := range strings.Split(s, sep) {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	},
	"b64": func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
	// digits keeps the digits of an id such as a QQ group number, so it
	// can stand unquoted as a JSON number; an id without digits becomes 0.
	"digits": func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			} else if b.Len() > 0 && (r == ',' || r == ' ') {
				break
			}
		}
		if b.Len() == 0 {
			return "0"
		}
		return b.String()
	},
}

func renderTemplate(src string, data map[string]any) (string, error) {
	t, err := template.New("t").Funcs(funcs).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", fmt.Errorf("template: %v", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("template: %v", err)
	}
	// A name the data does not have prints as "<no value>": nothing is
	// better in an alert.
	return strings.ReplaceAll(b.String(), "<no value>", ""), nil
}

// ValidateTemplate parses a body template (for configuration checks).
func ValidateTemplate(src string) error {
	_, err := template.New("t").Funcs(funcs).Parse(src)
	return err
}

// checkTemplate tries a template an operator wrote on a made-up message,
// with the channel's options: it must parse, and every name it uses must
// exist. extra adds names (StatusText for SMS parameters is always there).
func checkTemplate(what, src string, o map[string]string) error {
	if src == "" || strings.Contains(src, "${") {
		return nil // references are resolved later
	}
	t, err := template.New("t").Funcs(funcs).Option("missingkey=error").Parse(src)
	if err != nil {
		return fmt.Errorf("%s: %s", what, templateError(err))
	}
	var b strings.Builder
	if err := t.Execute(&b, messageData(sampleMessage(nil), o)); err != nil {
		return fmt.Errorf("%s: %s", what, templateError(err))
	}
	return nil
}

// templateError shortens text/template's error to what went wrong.
func templateError(err error) string {
	s := err.Error()
	if i := strings.Index(s, "map has no entry for key "); i >= 0 {
		return "unknown name " + strings.Trim(s[i+len("map has no entry for key "):], `"`) + " (see the list of names in the manual)"
	}
	s = strings.TrimPrefix(s, "template: ")
	if _, rest, ok := strings.Cut(s, ": "); ok && strings.HasPrefix(s, "t:") {
		s = rest
	}
	return s
}

// PreviewTemplate is what a channel with this template= sends for an
// example problem, for the form that edits it: the title and the body, or
// why the template does not work. An empty template shows the built-in
// text.
func PreviewTemplate(tpl, lang, instance string) (title, body string, err error) {
	m := sampleMessage(&Message{Lang: lang, Instance: instance})
	if strings.TrimSpace(tpl) == "" {
		title, body = Render(m)
		return title, body, nil
	}
	o := map[string]string{"template": tpl}
	if err := checkTemplate("template", templateLines(tpl), o); err != nil {
		return "", "", err
	}
	title, body = textOf(o, m)
	return title, body, nil
}
