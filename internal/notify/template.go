package notify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// templateData exposes the message (capitalised names) and the channel's
// options (lower case, dashes kept: use index for those) to templates.
// With escapeJSON, string values are pre-escaped for use inside JSON
// string literals; {{raw .X}} undoes it.
func templateData(m *Message, o map[string]string, escapeJSON bool) map[string]any {
	title, body := Render(m)
	esc := func(s string) string {
		if !escapeJSON {
			return s
		}
		b, _ := json.Marshal(s)
		return string(b[1 : len(b)-1])
	}
	d := map[string]any{
		"Title": esc(title), "Body": esc(body), "Text": esc(title + "\n" + body), "Kind": m.Kind, "Priority": m.Priority,
		"Monitor": esc(m.Monitor), "Name": esc(m.Title), "Type": m.Type, "Target": esc(m.Target), "Depth": m.Depth,
		"Status": m.Status, "Reason": m.Reason, "Detail": esc(sanitize(m.Detail, detailCap)), "Quorum": esc(m.Quorum),
		"Count": m.Count, "Max": m.Max, "IncidentID": m.IncidentID, "GlobalIncidentID": esc(m.GlobalIncidentID), "Duration": m.Duration, "DetailURL": esc(m.DetailURL),
		"AckURL": esc(m.DetailURL), "Tags": m.Tags, "Time": m.Time, "Delayed": m.Delayed, "DryRun": m.DryRun,
		"Instance": esc(m.Instance), "Site": esc(m.Site), "DeliveryID": m.DeliveryID,
	}
	for k, v := range o {
		if _, taken := d[k]; !taken {
			d[k] = esc(v)
		}
	}
	return d
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
	return b.String(), nil
}

// ValidateTemplate parses a body template (for configuration checks).
func ValidateTemplate(src string) error {
	_, err := template.New("t").Funcs(funcs).Parse(src)
	return err
}
