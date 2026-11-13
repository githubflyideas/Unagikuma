package notify

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Templates written for Uptime Kuma use Liquid ({% if %}…{% endif %},
// {{ monitorJSON.name }}). People paste them from Kuma's webhook form, so
// they are read here as well: the Liquid tags become Go template actions and
// Kuma's names are part of the template data (kumaData).

var (
	kumaTag    = regexp.MustCompile(`\{%-?\s*(.*?)\s*-?%\}`)
	kumaOutput = regexp.MustCompile(`\{\{-?\s*([A-Za-z_][\w.]*)\s*-?\}\}`)
	kumaCond   = regexp.MustCompile(`^(.+?)\s*(==|!=|>=|<=|>|<)\s*(.+)$`)
	kumaIdent  = regexp.MustCompile(`^[A-Za-z_][\w.]*$`)
)

// kumaRoots are the names Kuma gives a template.
var kumaRoots = map[string]bool{"monitorJSON": true, "heartbeatJSON": true, "msg": true, "name": true, "status": true,
	"hostnameOrURL": true, "NAME": true, "STATUS": true, "HOSTNAME_OR_URL": true}

// isKumaTemplate reports whether src is written the Uptime Kuma way.
func isKumaTemplate(src string) bool {
	if strings.Contains(src, "{%") {
		return true
	}
	for _, m := range kumaOutput.FindAllStringSubmatch(src, -1) {
		if kumaRoots[strings.SplitN(m[1], ".", 2)[0]] {
			return true
		}
	}
	return false
}

// kumaToGo turns a Kuma (Liquid) template into a Go template. A template
// that is not written the Kuma way is returned unchanged.
func kumaToGo(src string) string {
	if !isKumaTemplate(src) {
		return src
	}
	src = kumaOutput.ReplaceAllStringFunc(src, func(s string) string {
		name := kumaOutput.FindStringSubmatch(s)[1]
		if !kumaRoots[strings.SplitN(name, ".", 2)[0]] {
			return s
		}
		return "{{." + name + "}}"
	})
	return kumaTag.ReplaceAllStringFunc(src, func(s string) string {
		body := strings.TrimSpace(kumaTag.FindStringSubmatch(s)[1])
		word, rest, _ := strings.Cut(body, " ")
		switch word {
		case "if":
			return "{{if " + kumaExpr(rest) + "}}"
		case "unless":
			return "{{if not " + kumaExpr(rest) + "}}"
		case "elsif":
			return "{{else if " + kumaExpr(rest) + "}}"
		case "else":
			return "{{else}}"
		case "endif", "endunless":
			return "{{end}}"
		}
		return s // anything else is left as it is and reported by the parser
	})
}

// kumaExpr turns a Liquid condition ("a == 1", "a and b") into a Go
// template pipeline.
func kumaExpr(e string) string {
	e = strings.TrimSpace(e)
	for _, op := range []string{" or ", " and "} {
		if l, r, ok := strings.Cut(e, op); ok {
			return strings.TrimSpace(op) + " (" + kumaExpr(l) + ") (" + kumaExpr(r) + ")"
		}
	}
	if m := kumaCond.FindStringSubmatch(e); m != nil {
		fn := map[string]string{"==": "eq", "!=": "ne", ">": "gt", "<": "lt", ">=": "ge", "<=": "le"}[m[2]]
		return fn + " " + kumaOperand(m[1]) + " " + kumaOperand(m[3])
	}
	return kumaOperand(e)
}

func kumaOperand(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "nil" || s == "null" || s == "empty" || s == "blank":
		return `""`
	case s == "true" || s == "false":
		return s
	case strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") && len(s) >= 2:
		return `"` + strings.ReplaceAll(s[1:len(s)-1], `"`, `\"`) + `"`
	case kumaIdent.MatchString(s):
		return "." + s
	}
	return s // numbers and "quoted" strings
}

// kumaData is what Kuma gives a template, made from the message: the
// monitor, the heartbeat (status 1 = up, 0 = down) and the message text.
func kumaData(m *Message, text string) map[string]any {
	host, port := kumaHost(m.Target)
	typ := strings.ToLower(m.Type)
	if typ == "tcp" {
		typ = "port"
	}
	status, word := 0, "🔴 Down"
	if m.Kind == "RECOVERY" {
		status, word = 1, "✅ Up"
	}
	at := m.Time
	if at.IsZero() {
		at = time.Now()
	}
	detail := m.Detail
	if detail == "" {
		detail = m.Reason
	}
	monitorURL := ""
	if strings.HasPrefix(m.Target, "http://") || strings.HasPrefix(m.Target, "https://") {
		monitorURL = m.Target
	}
	address := firstOf(monitorURL, host)
	if port != "" && monitorURL == "" {
		address = host + ":" + port
	}
	name := firstOf(m.Title, m.Monitor)
	return map[string]any{
		"monitorJSON": map[string]any{"id": m.Monitor, "name": name, "type": typ, "url": monitorURL, "hostname": host, "port": port,
			"description": m.Detail, "tags": m.Tags},
		"heartbeatJSON": map[string]any{"status": status, "msg": detail, "time": at.UTC().Format("2006-01-02 15:04:05.000"),
			"localDateTime": at.Local().Format("2006-01-02 15:04:05"), "timezone": at.Local().Location().String(), "important": true},
		"msg": text, "name": name, "status": word, "hostnameOrURL": address,
		"NAME": name, "STATUS": word, "HOSTNAME_OR_URL": address,
	}
}

// kumaHost splits a monitor's target into the host and port Kuma reports.
func kumaHost(target string) (host, port string) {
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return u.Hostname(), u.Port()
	}
	if h, p, err := net.SplitHostPort(target); err == nil {
		return h, p
	}
	return target, ""
}
