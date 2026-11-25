package importer

import (
	"errors"
	"net"
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

// adapterNote is something about a monitor that was imported anyway:
// what was not carried over, said in the report.
type adapterNote struct{ why, todo string }

// adapterMonitor is one monitor of a bounded adapter (Gatus, Checkmate)
// before it becomes a record. Nothing about it is dropped quietly:
// what cannot be carried at all lands in whys (the monitor is left
// out, with every reason), what is carried approximately or not at
// all lands in notes (the monitor is imported, and the report says).
type adapterMonitor struct {
	name, group, target, method, body, interval, timeout, expect, bearer, sourceType string
	// typ is the record type to build: "" or "HTTP", "PING", "TCP", "DNS".
	typ string
	// HTTP extras (Gatus conditions): body keyword, JSON path check,
	// certificate days.
	keyword, jsonPath, jsonExpect, jsonExpectNum, certDays string
	absent                                                 bool
	// DNS extras: the name to look up, its type, and (in expect) the
	// answer that must come back.
	query, qtype string
	// attempts overrides the record's attempts (Checkmate's status
	// window mapping); "" keeps the adapter default of 1.
	attempts                     string
	headers                      map[string]string
	disabled, insecure, noFollow bool
	whys                         []string
	notes                        []adapterNote
}

// block adds a reason this monitor cannot be imported.
func (m *adapterMonitor) block(why string) { m.whys = append(m.whys, why) }

// note records something that was not carried over, or was carried
// approximately, about a monitor that is imported anyway.
func (m *adapterMonitor) note(why, todo string) {
	m.notes = append(m.notes, adapterNote{why, todo})
}

func (m *adapterMonitor) blocked() bool { return len(m.whys) > 0 }

// joinReasons writes blocking reasons as sentences on one line.
func joinReasons(whys []string) string {
	parts := make([]string, len(whys))
	for i, w := range whys {
		w = strings.TrimSpace(w)
		if w != "" && !strings.HasSuffix(w, ".") {
			w += "."
		}
		parts[i] = w
	}
	return strings.Join(parts, " ")
}

func adapterResult(source string, monitors []adapterMonitor, existing *config.Config, o Options) *Result {
	r := &Result{Source: source, File: "upload", Area: o.Area, When: o.now(), Secrets: map[string]string{}, totalNow: o.TotalNow, totalLimit: o.TotalLimit}
	used := map[string]bool{}
	for _, m := range monitors {
		recType := m.typ
		if recType == "" {
			recType = "HTTP"
		}
		it := Item{Kind: "monitor", Source: m.name, Group: m.group, Type: typeLabel(recType), Status: StatusLeftOut, Todo: "Add the check by hand if you need it."}
		// A title in monitors.conf is 1 to 64 characters.
		if !cleanLabel(m.name) || m.name == "" || len([]rune(m.name)) > 64 || !cleanLabel(m.group) {
			it.Source = "(invalid name)"
			it.Group = ""
			m.block("invalid name or group")
		}
		if m.sourceType != "" && !strings.EqualFold(m.sourceType, "http") && m.typ == "" && cleanLabel(m.sourceType) {
			it.Type = m.sourceType
		}
		keys := m.check()
		name := ""
		if !m.blocked() {
			// Only a monitor that will be emitted needs a name; naming
			// a blocked one would pile a spurious collision reason on
			// top of the reasons that already keep it out.
			name = newNamer(nil).take(m.name, "endpoint")
			if used[name] || existing != nil && (existing.HasName(name) || existing.MName == name) {
				// Imported before: the same check under the same name is
				// already here, so a second run leaves it as it is.
				if em := existingMonitor(existing, name); em != nil && !used[name] && em.Type == recType && em.Target == m.target {
					used[name] = true
					it.Name, it.Target, it.Status, it.Todo = name, m.target, StatusSkipped, ""
					r.add(it)
					continue
				}
				m.block("record name collision; rename the source monitor explicitly")
			}
			used[name] = true
		}
		if m.blocked() {
			it.Why = joinReasons(m.whys)
			r.add(it)
			continue
		}
		rec := adapterRecord(source, name, m, keys, r, o)
		it.Name, it.Target, it.Status, it.Todo = name, m.target, StatusImported, ""
		for _, n := range m.notes {
			it.attention(n.why, n.todo)
		}
		r.sections = append(r.sections, section{title: "Monitor", recs: []config.RecordSpec{rec}})
		r.Monitors++
		r.add(it)
	}
	r.finish()
	return r
}

// check fills in the defaults and collects a reason for anything that
// would not behave the same here; it returns the header names, sorted.
func (m *adapterMonitor) check() []string {
	switch m.typ {
	case "PING":
		if m.target == "" || strings.Contains(m.target, "://") || strings.ContainsAny(m.target, "/ \t") || placeholder(m.target) {
			m.block("only a plain host name or address, without a placeholder, can be pinged")
		}
	case "TCP":
		host, port, err := net.SplitHostPort(m.target)
		p, perr := strconv.Atoi(port)
		if err != nil || host == "" || perr != nil || p < 1 || p > 65535 || placeholder(m.target) {
			m.block("only a host:port target without a placeholder can be checked")
		}
	case "DNS":
		host := m.target
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" || strings.Contains(host, "://") || strings.ContainsAny(host, "/ \t") || placeholder(m.target) {
			m.block("only a DNS server address without a placeholder can be checked")
		}
	default: // HTTP
		u, err := url.Parse(m.target)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || placeholder(m.target) {
			m.block("only HTTP(S) URLs without credentials, fragments or placeholders are supported")
		}
		if m.method == "" {
			m.method = "GET"
		}
		if !contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, m.method) {
			m.block("unsupported HTTP method")
		}
		if placeholder(m.body) || placeholder(m.bearer) {
			m.block("unresolved secret/environment/body placeholder")
		}
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
			m.block("interval/timeout must be whole seconds between 1s and 24h")
		}
	}
	if m.attempts != "" {
		if a, err := strconv.Atoi(m.attempts); err != nil || a < 1 || a > 100 {
			m.block("attempts must be a number between 1 and 100")
		}
	}
	keys := make([]string, 0, len(m.headers))
	lower := map[string]bool{}
	for k, v := range m.headers {
		if m.typ != "" && m.typ != "HTTP" {
			continue // headers belong to HTTP records
		}
		lk := strings.ToLower(k)
		if !optKeyOK("header."+lk) || lower[lk] || strings.ContainsAny(v, "\r\n") || placeholder(v) {
			m.block("unsupported or duplicate header, or unresolved placeholder")
		}
		lower[lk] = true
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// adapterRecord writes the record of a checked monitor; credentials
// (body, bearer token, headers) become secrets.
func adapterRecord(source, name string, m adapterMonitor, keys []string, r *Result, o Options) config.RecordSpec {
	typ := m.typ
	if typ == "" {
		typ = "HTTP"
	}
	rec := config.RecordSpec{Name: name, Type: typ, Target: m.target}
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
	switch typ {
	case "HTTP":
		add("method", m.method)
		add("expect", m.expect)
		add("keyword", m.keyword)
		if m.absent {
			add("absent", "yes")
		}
		add("json-path", m.jsonPath)
		add("json-expect", m.jsonExpect)
		add("json-expect-num", m.jsonExpectNum)
		add("cert-days", m.certDays)
	case "DNS":
		add("query", m.query)
		add("qtype", m.qtype)
		add("expect", m.expect)
	}
	add("timeout", m.timeout)
	if m.attempts != "" {
		add("attempts", m.attempts)
	} else {
		add("attempts", "1")
	}
	if m.disabled {
		add("disabled", "yes")
	}
	if typ == "HTTP" {
		for _, flag := range []struct {
			on  bool
			key string
		}{{m.insecure, "insecure"}, {m.noFollow, "no-follow"}} {
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
	}
	return rec
}

// ActivationError is checked by both CLI and application before any writes.
func (r *Result) ActivationError() error {
	if r.Source == "Unagikuma" {
		for _, it := range r.Items {
			if it.Kind == "secret" && it.Status == StatusLeftOut {
				return errors.New("import blocked: add the secrets listed in the preview first")
			}
		}
	}
	// The bounded adapters (Gatus, Checkmate) import what they can and
	// list the rest in the report; a monitor that was left out no
	// longer blocks the ones that were carried over faithfully.
	return nil
}

// existingMonitor is the monitor of that name in the configuration, if any.
func existingMonitor(c *config.Config, name string) *config.Monitor {
	if c == nil {
		return nil
	}
	return c.FindMonitor(name)
}
