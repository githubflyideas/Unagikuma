package importer

import (
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/githubflyideas/unagikuma/internal/config"
)

const AdapterMaxBytes = 1 << 20

func placeholder(s string) bool {
	return strings.Contains(s, "$") || strings.Contains(s, "{{") || strings.Contains(s, "[CONTEXT]") || strings.Contains(s, "[ENDPOINT_") || strings.Contains(s, "[LOCAL_ADDRESS]") || strings.Contains(s, "[RANDOM_STRING_")
}
func cleanLabel(s string) bool {
	return len(s) <= 512 && !placeholder(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

type adapterMonitor struct {
	name, group, target, method, body, interval, timeout, expect, bearer, why, sourceType string
	headers                                                                               map[string]string
	disabled, insecure, noFollow                                                          bool
}

func adapterResult(source string, monitors []adapterMonitor, existing *config.Config, o Options) *Result {
	r := &Result{Source: source, File: "upload", Area: o.Area, When: o.now(), Secrets: map[string]string{}}
	used := map[string]bool{}
	for _, m := range monitors {
		it := Item{Kind: "monitor", Source: m.name, Group: m.group, Type: "HTTP", Status: StatusLeftOut, Todo: "Resolve the unsupported configuration and preview again; apply is blocked."}
		// A title in monitors.conf is 1 to 64 characters.
		if !cleanLabel(m.name) || m.name == "" || len([]rune(m.name)) > 64 || !cleanLabel(m.group) {
			it.Source = "(invalid name)"
			it.Group = ""
			m.why = "invalid name or group"
		}
		if m.sourceType != "" && !strings.EqualFold(m.sourceType, "http") && cleanLabel(m.sourceType) {
			it.Type = m.sourceType
		}
		name := newNamer(nil).take(m.name, "endpoint")
		if used[name] || existing != nil && (existing.HasName(name) || existing.MName == name) {
			// Imported before: the same check under the same name is
			// already here, so a second run leaves it as it is.
			if em := existingMonitor(existing, name); em != nil && !used[name] && em.Type == "HTTP" && em.Target == m.target {
				used[name] = true
				it.Name, it.Target, it.Status, it.Todo = name, m.target, StatusSkipped, ""
				r.add(it)
				continue
			}
			m.why = "record name collision; rename the source monitor explicitly"
		}
		used[name] = true
		keys := m.check()
		if m.why != "" {
			it.Why = m.why
			r.add(it)
			continue
		}
		rec := adapterRecord(source, name, m, keys, r, o)
		it.Name, it.Target, it.Status, it.Todo = name, m.target, StatusImported, ""
		r.sections = append(r.sections, section{title: "Monitor", recs: []config.RecordSpec{rec}})
		r.Monitors++
		r.add(it)
	}
	r.finish()
	return r
}

// check fills in the defaults and sets why for anything that would not
// behave the same here; it returns the header names, sorted.
func (m *adapterMonitor) check() []string {
	u, err := url.Parse(m.target)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || placeholder(m.target) {
		m.why = "only HTTP(S) URLs without credentials, query strings, fragments or placeholders are supported"
	}
	if m.method == "" {
		m.method = "GET"
	}
	if !contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, m.method) {
		m.why = "unsupported HTTP method"
	}
	if m.interval == "" {
		m.interval = "60s"
	}
	if m.timeout == "" {
		m.timeout = "10s"
	}
	for _, s := range []string{m.interval, m.timeout} {
		d, e := time.ParseDuration(s)
		if e != nil || d < time.Second || d > 24*time.Hour || d%time.Second != 0 {
			m.why = "interval/timeout must be whole seconds between 1s and 24h"
		}
	}
	if placeholder(m.body) || placeholder(m.bearer) {
		m.why = "unresolved secret/environment/body placeholder"
	}
	keys := make([]string, 0, len(m.headers))
	lower := map[string]bool{}
	for k, v := range m.headers {
		lk := strings.ToLower(k)
		if !optKeyOK("header."+lk) || lower[lk] || strings.ContainsAny(v, "\r\n") || placeholder(v) {
			m.why = "unsupported or duplicate header, or unresolved placeholder"
		}
		lower[lk] = true
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// adapterRecord writes the HTTP record of a checked monitor; credentials
// (body, bearer token, headers) become secrets.
func adapterRecord(source, name string, m adapterMonitor, keys []string, r *Result, o Options) config.RecordSpec {
	rec := config.RecordSpec{Name: name, Type: "HTTP", Target: m.target}
	d, _ := time.ParseDuration(m.interval)
	rec.Interval = strconv.FormatInt(int64(d/time.Second), 10) + "s"
	add := func(k, v string) {
		if v != "" {
			rec.Opts = append(rec.Opts, config.Opt{Key: k, Value: v})
		}
	}
	secret := func(k, v string) string {
		base := strings.ToLower(source) + "_" + strings.ReplaceAll(name, "-", "_") + "_" + strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(k), "-", "_"), ".", "_")
		n := base
		for i := 2; r.Secrets[n] != "" || o.SecretExists != nil && o.SecretExists(n); i++ {
			n = base + "_" + strconv.Itoa(i)
		}
		r.Secrets[n] = v
		return "${secret:" + n + "}"
	}
	add("title", m.name)
	add("group", m.group)
	add("method", m.method)
	add("expect", m.expect)
	add("timeout", m.timeout)
	add("attempts", "1")
	for _, flag := range []struct {
		on  bool
		key string
	}{{m.disabled, "disabled"}, {m.insecure, "insecure"}, {m.noFollow, "no-follow"}} {
		if flag.on {
			add(flag.key, "yes")
		}
	}
	if m.body != "" {
		add("body", secret("body", m.body))
	}
	if m.bearer != "" {
		add("bearer", secret("bearer", m.bearer))
	}
	for _, k := range keys {
		add("header."+strings.ToLower(k), secret(k, m.headers[k]))
	}
	return rec
}

// ActivationError is checked by both CLI and application before any writes.
// A partial migration must never activate a broadened success predicate.
func (r *Result) ActivationError() error {
	if r.Source != "Gatus" && r.Source != "Checkmate" {
		return nil
	}
	for _, it := range r.Items {
		if it.Status == StatusLeftOut {
			return errors.New("import blocked: resolve every not-imported item in the preview before applying")
		}
	}
	return nil
}

// existingMonitor is the monitor of that name in the configuration, if any.
func existingMonitor(c *config.Config, name string) *config.Monitor {
	if c == nil {
		return nil
	}
	return c.FindMonitor(name)
}
