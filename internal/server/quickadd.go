package server

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/keywords"
)

var nameUnsafe = regexp.MustCompile(`[^a-z0-9_.-]+`)

// quickRecord parses one quick-add line:
//
//	https://example.com            → HTTP
//	tcp db.lan:5432 / db.lan:5432  → TCP
//	udp 10.1.1.1:snmp / 10.1.1.1:ntp → UDP keyword
//	dns|ntp|snmp|syslog host[:port] → shorthand
//	ping host / host                → PING
//
// followed by optional key=value options (name= sets the record name).
func quickRecord(line string, used map[string]bool) (config.RecordSpec, error) {
	fields := strings.Fields(line)
	var spec config.RecordSpec
	var opts []config.Opt
	var words []string
	for _, f := range fields {
		if k, v, ok := strings.Cut(f, "="); ok && k != "" && !strings.Contains(k, "/") {
			if strings.ToLower(k) == "name" {
				spec.Name = v
				continue
			}
			opts = append(opts, config.Opt{Key: strings.ToLower(k), Value: strings.Trim(v, `"`)})
			continue
		}
		words = append(words, f)
	}
	if len(words) == 0 {
		return spec, fmt.Errorf("nothing to monitor")
	}
	typ, target := "", ""
	switch strings.ToLower(words[0]) {
	case "http", "https", "tcp", "udp", "ping", "dns", "ntp", "snmp", "syslog", "redis", "postgres", "mysql", "grpc", "docker":
		if len(words) < 2 {
			return spec, fmt.Errorf("%s needs a target", words[0])
		}
		typ, target = strings.ToUpper(words[0]), words[1]
		if typ == "HTTPS" {
			typ = "HTTP"
		}
	default:
		target = words[0]
	}
	if typ == "" {
		switch {
		case strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://"):
			typ = "HTTP"
		default:
			if _, p, err := net.SplitHostPort(target); err == nil {
				if _, err := strconv.Atoi(p); err == nil {
					typ = "TCP"
				} else if _, ok := keywords.Lookup(p); ok {
					typ = "UDP"
				} else {
					return spec, fmt.Errorf("unknown keyword %q", p)
				}
			} else {
				typ = "PING"
			}
		}
	}
	if typ == "HTTP" && !strings.Contains(target, "://") {
		target = "https://" + target
	}
	spec.Type, spec.Target, spec.Opts = typ, target, opts
	if spec.Name == "" {
		spec.Name = deriveName(typ, target, used)
	}
	if !config.ValidName(spec.Name) {
		return spec, fmt.Errorf("invalid name %q", spec.Name)
	}
	return spec, nil
}

func deriveName(typ, target string, used map[string]bool) string {
	base := target
	suffix := ""
	switch typ {
	case "HTTP":
		if u, err := url.Parse(target); err == nil {
			base = u.Hostname()
			if p := strings.Trim(u.Path, "/"); p != "" {
				suffix = "-" + strings.Split(p, "/")[0]
			}
		}
	case "TCP", "UDP":
		if h, p, err := net.SplitHostPort(target); err == nil {
			base, suffix = h, "-"+p
		}
	case "REDIS", "POSTGRES", "MYSQL", "GRPC":
		h := target
		if hh, _, err := net.SplitHostPort(target); err == nil {
			h = hh
		}
		base, suffix = h, "-"+strings.ToLower(typ)
	case "DNS", "NTP", "SNMP", "SYSLOG":
		h := target
		if hh, _, err := net.SplitHostPort(target); err == nil {
			h = hh
		}
		base, suffix = h, "-"+strings.ToLower(typ)
	}
	base = strings.TrimPrefix(base, "www.")
	name := strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(base+suffix), "-"), "-.")
	if len(name) > 60 {
		name = name[:60]
	}
	if name == "" || !config.ValidName(name) {
		name = strings.ToLower(typ)
	}
	out := name
	for i := 2; used[out]; i++ {
		out = fmt.Sprintf("%s-%d", name, i)
	}
	return out
}
