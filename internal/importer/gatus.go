package importer

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
)

var errGatusShape = errors.New("Gatus document requires an endpoints array with typed fields")

// Gatus imports a single local YAML document, never resolving includes or env.
func Gatus(data []byte, existing *config.Config, o Options) (*Result, error) {
	root, err := boundedYAML(data)
	if err != nil {
		return nil, err
	}
	if root.kind != yMap {
		return nil, errGatusShape
	}
	eps := root.get("endpoints")
	if eps == nil || eps.kind != ySeq {
		return nil, errGatusShape
	}
	if len(eps.items) > 1000 {
		return nil, errors.New("maximum 1000 endpoints")
	}
	var mons []adapterMonitor
	for _, e := range eps.items {
		m, err := gatusEndpoint(e)
		if err != nil {
			return nil, err
		}
		mons = append(mons, m)
	}
	r := adapterResult("Gatus", mons, existing, o)
	var others []string
	for _, k := range root.keys {
		if k != "endpoints" {
			others = append(others, k)
		}
	}
	if len(others) > 0 {
		r.add(Item{Kind: "configuration", Source: "top-level settings", Status: StatusLeftOut,
			Why:  "Only endpoints are imported; these top-level settings were not carried over: " + strings.Join(others, ", ") + ".",
			Todo: "Set up alerting, storage and the web UI here instead (Settings); external endpoints and suites have no equivalent."})
		r.finish()
	}
	return r, nil
}

// gatusEndpoint maps one endpoint. Wrongly typed known fields make the
// whole file unreadable; everything else is decided per endpoint and
// said in the report.
func gatusEndpoint(e *ynode) (adapterMonitor, error) {
	var m adapterMonitor
	if e.kind != yMap {
		return m, errGatusShape
	}
	var conditions []string
	var dns *ynode
	var ignored []string
	for i, k := range e.keys {
		if err := gatusField(&m, &conditions, &dns, &ignored, k, e.vals[i]); err != nil {
			return m, err
		}
	}
	if len(ignored) > 0 {
		m.note("These endpoint settings have no equivalent and were not carried over: "+strings.Join(ignored, ", ")+".", "")
	}
	gatusResolve(&m, conditions, dns)
	return m, nil
}

func yStr(n *ynode) (string, error) {
	if n.kind != yScalar {
		return "", errGatusShape
	}
	return n.value, nil
}

func yBool(n *ynode) (bool, error) {
	if n.kind != yScalar || n.quoted {
		return false, errGatusShape
	}
	switch strings.ToLower(n.value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, errGatusShape
}

// gatusField reads one field of an endpoint into m.
func gatusField(m *adapterMonitor, conditions *[]string, dns **ynode, ignored *[]string, k string, v *ynode) error {
	var err error
	if dst := map[string]*string{"name": &m.name, "group": &m.group, "url": &m.target, "interval": &m.interval,
		"method": &m.method, "body": &m.body}[k]; dst != nil {
		*dst, err = yStr(v)
		return err
	}
	switch k {
	case "enabled":
		var on bool
		on, err = yBool(v)
		m.disabled = !on
	case "headers":
		return gatusHeaders(m, v)
	case "conditions":
		if v.kind != ySeq {
			return errGatusShape
		}
		for _, c := range v.items {
			s, err := yStr(c)
			if err != nil {
				return err
			}
			*conditions = append(*conditions, s)
		}
	case "client":
		return gatusClient(m, v)
	case "dns":
		if v.kind != yMap {
			return errGatusShape
		}
		*dns = v
	case "alerts":
		m.note("Its alerts were not carried over.", "Choose who is notified under Alerting › Policies.")
	default:
		*ignored = append(*ignored, k)
	}
	return err
}

func gatusHeaders(m *adapterMonitor, v *ynode) error {
	if v.kind != yMap {
		return errGatusShape
	}
	m.headers = map[string]string{}
	for j, hk := range v.keys {
		var err error
		if m.headers[hk], err = yStr(v.vals[j]); err != nil {
			return err
		}
	}
	return nil
}

func gatusClient(m *adapterMonitor, v *ynode) error {
	if v.kind != yMap {
		return errGatusShape
	}
	var ignored []string
	for j, ck := range v.keys {
		var err error
		switch ck {
		case "insecure":
			m.insecure, err = yBool(v.vals[j])
		case "ignore-redirect":
			m.noFollow, err = yBool(v.vals[j])
		case "timeout":
			m.timeout, err = yStr(v.vals[j])
		case "tls", "oauth2":
			// Authentication material (certificate files, client
			// credentials) cannot be reconstructed from the file, and
			// the check would behave differently without it.
			m.block("client." + ck + " (TLS files or authentication) is not supported")
		default:
			ignored = append(ignored, "client."+ck)
		}
		if err != nil {
			return err
		}
	}
	if len(ignored) > 0 {
		m.note("These client settings were not carried over: "+strings.Join(ignored, ", ")+".", "")
	}
	return nil
}

// gatusResolve decides what kind of check an endpoint is (from its URL
// and its dns block) and maps its conditions.
func gatusResolve(m *adapterMonitor, conditions []string, dns *ynode) {
	raw := m.target
	scheme, _, _ := strings.Cut(strings.ToLower(raw), "://")
	if !strings.Contains(raw, "://") {
		scheme = ""
	}
	switch {
	case dns != nil:
		m.typ = "DNS"
		m.target = gatusHost(raw)
		gatusDNSBlock(m, dns)
	case scheme == "icmp":
		m.typ = "PING"
		m.target = gatusHostname(raw)
	case scheme == "tcp":
		m.typ = "TCP"
		m.target = gatusHost(raw)
	case scheme == "http" || scheme == "https":
		m.typ = "HTTP"
	case scheme == "udp" || scheme == "dns":
		m.typ = "HTTP"
		m.block("a " + scheme + ":// endpoint needs a dns block (query-name and query-type) to be imported as a DNS check")
	case scheme == "":
		m.typ = "HTTP"
		if raw != "" && onlyConnected(conditions) {
			m.typ = "PING"
		} else {
			m.block("the URL has no scheme (http://, https://, icmp:// or tcp://), so what to check is unclear")
		}
	default:
		m.typ = "HTTP"
		m.block("the URL scheme " + scheme + ":// has no equivalent check here")
	}
	gatusConditions(m, conditions)
}

// gatusHost is the host (with port, if any) of a URL or bare address.
func gatusHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// gatusHostname is the host name of a URL, without scheme, port or path.
func gatusHostname(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/:"); i >= 0 {
		s = s[:i]
	}
	return s
}

// gatusDNSBlock reads an endpoint's dns block: the name it looks up.
func gatusDNSBlock(m *adapterMonitor, dns *ynode) {
	name, typ := dns.get("query-name"), dns.get("query-type")
	if name == nil || name.kind != yScalar || name.value == "" {
		m.block("a DNS endpoint without dns.query-name cannot be imported")
		return
	}
	m.query = name.value
	if typ != nil && typ.kind == yScalar && typ.value != "" && !strings.EqualFold(typ.value, "A") {
		m.qtype = strings.ToUpper(typ.value)
	}
}

var (
	gatusStatusRe = regexp.MustCompile(`^\[STATUS\]\s*(==|!=|<=|>=|<|>)\s*([0-9]{1,3})$`)
	gatusBodyRe   = regexp.MustCompile(`^\[BODY\]\s*(==|!=)\s*(.+)$`)
	gatusPathRe   = regexp.MustCompile(`^\[BODY\]((?:\.[A-Za-z0-9_-]+|\[[0-9]+\])+)\s*(==|!=|<=|>=|<|>)\s*(.+)$`)
	gatusRespRe   = regexp.MustCompile(`^\[RESPONSE_TIME\]\s*(==|!=|<=|>=|<|>)\s*[0-9]+$`)
	gatusCertRe   = regexp.MustCompile(`^\[CERTIFICATE_EXPIRATION\]\s*(==|!=|<=|>=|<|>)\s*(\S+)$`)
	gatusConnRe   = regexp.MustCompile(`^\[CONNECTED\]\s*(==|!=)\s*(true|false)$`)
	gatusRcodeRe  = regexp.MustCompile(`^\[DNS_RCODE\]\s*(==|!=)\s*([A-Za-z]+)$`)
	gatusPatRe    = regexp.MustCompile(`^pat\((.*)\)$`)
)

// onlyConnected reports whether every condition is [CONNECTED] == true.
func onlyConnected(conditions []string) bool {
	if len(conditions) == 0 {
		return false
	}
	for _, c := range conditions {
		m := gatusConnRe.FindStringSubmatch(strings.TrimSpace(c))
		if m == nil || (m[1] == "==") != (m[2] == "true") {
			return false
		}
	}
	return true
}

// gatusConditions maps an endpoint's conditions onto the record. Gatus
// conditions are conjunctive: all of them must hold. What maps is
// mapped; what has no equivalent is named in a note, except the few
// shapes whose meaning would invert or void the check — those keep
// the endpoint out, with the reason.
func gatusConditions(m *adapterMonitor, conditions []string) {
	if len(conditions) == 0 {
		m.block("it has no conditions, so what counts as up is undefined")
		return
	}
	cs := &gatusCond{m: m}
	for _, c := range conditions {
		cs.one(strings.TrimSpace(c))
	}
	if cs.statusTouched && m.typ == "HTTP" {
		if exp, ok := statusExpect(cs.codes); ok {
			m.expect = exp
		} else {
			m.block("the [STATUS] conditions contradict each other: no status code satisfies all of them")
		}
	}
}

// gatusCond carries the condition mapping of one endpoint.
type gatusCond struct {
	m             *adapterMonitor
	codes         []bool // accepted status codes, once a [STATUS] condition appears
	statusTouched bool
	keywordSet    bool
	pathSet       bool
}

func (cs *gatusCond) one(cond string) {
	m := cs.m
	switch {
	case gatusStatusRe.MatchString(cond):
		cs.status(cond)
	case gatusConnRe.MatchString(cond):
		cs.connected(cond)
	case gatusRespRe.MatchString(cond):
		m.note("The condition "+strconv.Quote(cond)+" was not carried over: there is no response-time condition here.", "")
	case gatusCertRe.MatchString(cond):
		cs.cert(cond)
	case gatusRcodeRe.MatchString(cond):
		cs.rcode(cond)
	case gatusPathRe.MatchString(cond):
		cs.bodyPath(cond)
	case gatusBodyRe.MatchString(cond):
		cs.body(cond)
	case strings.HasPrefix(cond, "len("):
		m.note("The condition "+strconv.Quote(cond)+" was not carried over: counting what a reply holds has no equivalent here.", "")
	default:
		m.block("unsupported condition: " + truncate(cond, 80))
	}
}

// status folds one [STATUS] condition into the accepted set.
func (cs *gatusCond) status(cond string) {
	m := cs.m
	if m.typ != "HTTP" {
		m.block("a [STATUS] condition cannot be checked on a " + strings.ToLower(typeLabel(m.typ)) + " endpoint")
		return
	}
	g := gatusStatusRe.FindStringSubmatch(cond)
	code, _ := strconv.Atoi(g[2])
	if code < 100 || code > 599 {
		m.block("the status code in " + strconv.Quote(cond) + " is not a status code")
		return
	}
	if !cs.statusTouched {
		cs.codes = make([]bool, 600)
		for i := 100; i < 600; i++ {
			cs.codes[i] = true
		}
		cs.statusTouched = true
	}
	keep := func(c int) bool {
		switch g[1] {
		case "==":
			return c == code
		case "!=":
			return c != code
		case "<":
			return c < code
		case "<=":
			return c <= code
		case ">":
			return c > code
		case ">=":
			return c >= code
		}
		return false
	}
	for c := 100; c < 600; c++ {
		cs.codes[c] = cs.codes[c] && keep(c)
	}
}

// statusExpect writes an accepted-code set the way expect= takes it:
// single codes and ranges, comma-separated.
func statusExpect(set []bool) (string, bool) {
	var parts []string
	for c := 100; c < 600; {
		if !set[c] {
			c++
			continue
		}
		lo := c
		for c < 600 && set[c] {
			c++
		}
		if c-1 == lo {
			parts = append(parts, strconv.Itoa(lo))
		} else {
			parts = append(parts, strconv.Itoa(lo)+"-"+strconv.Itoa(c-1))
		}
	}
	return strings.Join(parts, ","), len(parts) > 0
}

// connected maps [CONNECTED]: on a ping or TCP endpoint it is the
// check itself; on HTTP and DNS endpoints a successful reply already
// proves it. Only its inverted form cannot be carried.
func (cs *gatusCond) connected(cond string) {
	g := gatusConnRe.FindStringSubmatch(cond)
	if (g[1] == "==") == (g[2] == "true") {
		return // [CONNECTED] == true (or != false): the check itself
	}
	cs.m.block("the endpoint counts as up when it cannot connect (an upside-down check), which has no equivalent here")
}

// cert maps [CERTIFICATE_EXPIRATION] > <whole days> to cert-days.
func (cs *gatusCond) cert(cond string) {
	m := cs.m
	g := gatusCertRe.FindStringSubmatch(cond)
	if g[1] == ">" && m.typ == "HTTP" && strings.HasPrefix(strings.ToLower(m.target), "https://") {
		if d, err := time.ParseDuration(g[2]); err == nil && d > 0 && d%(24*time.Hour) == 0 {
			m.certDays = strconv.Itoa(int(d / (24 * time.Hour)))
			return
		}
	}
	m.note("The condition "+strconv.Quote(cond)+" was not carried over: only a whole number of days on an HTTPS endpoint maps to cert-days here.", "")
}

// rcode maps [DNS_RCODE]: a DNS check here fails on any error reply,
// which is exactly == NOERROR.
func (cs *gatusCond) rcode(cond string) {
	m := cs.m
	g := gatusRcodeRe.FindStringSubmatch(cond)
	switch {
	case m.typ != "DNS":
		m.note("The condition "+strconv.Quote(cond)+" was not carried over: it belongs to a DNS endpoint.", "")
	case g[1] == "==" && strings.EqualFold(g[2], "NOERROR"):
		// The DNS check itself: an error reply is a failure.
	case g[1] == "==":
		m.block("only [DNS_RCODE] == NOERROR can be checked: a DNS check here fails on any error reply, so expecting " + strings.ToUpper(g[2]) + " has no equivalent")
	default:
		m.block("an inverted [DNS_RCODE] condition has no equivalent here")
	}
}

// bodyPath maps [BODY].path comparisons onto the one JSON path a
// record can check; a second, different path is named as not carried.
func (cs *gatusCond) bodyPath(cond string) {
	m := cs.m
	if m.typ != "HTTP" {
		m.block("a [BODY] condition cannot be checked on a " + strings.ToLower(typeLabel(m.typ)) + " endpoint")
		return
	}
	g := gatusPathRe.FindStringSubmatch(cond)
	path := strings.TrimPrefix(g[1], ".")
	op, val := g[2], unquote(strings.TrimSpace(g[3]))
	if cs.pathSet {
		m.note("The condition "+strconv.Quote(cond)+" was not carried over: a check here can look at one place in the reply (json-path is already set).", "")
		return
	}
	switch {
	case op == "==":
		m.jsonPath, m.jsonExpect = path, val
	case op == "!=":
		m.note("The condition "+strconv.Quote(cond)+" was not carried over: a value at a place in the reply can be required, but not forbidden.", "")
		return
	default:
		if _, err := strconv.ParseFloat(val, 64); err != nil {
			m.note("The condition "+strconv.Quote(cond)+" was not carried over: the comparison is not a number.", "")
			return
		}
		m.jsonPath, m.jsonExpectNum = path, op+val
	}
	cs.pathSet = true
}

// body maps whole-body conditions: [BODY] == pat(*text*) is a text the
// reply must contain (keyword=), a plain == on an HTTP endpoint is
// searched the same way (Gatus compares the whole body; the report
// says so), and on a DNS endpoint the body is the answer, which
// expect= compares exactly.
func (cs *gatusCond) body(cond string) {
	m := cs.m
	g := gatusBodyRe.FindStringSubmatch(cond)
	op, val := g[1], unquote(strings.TrimSpace(g[2]))
	if m.typ == "DNS" {
		if op == "==" && m.expect == "" {
			m.expect = val
		} else if op == "==" {
			m.note("The condition "+strconv.Quote(cond)+" was not carried over: a DNS check here can require one answer (expect is already set).", "")
		} else {
			m.note("The condition "+strconv.Quote(cond)+" was not carried over: a DNS answer can be required, but not forbidden.", "")
		}
		return
	}
	if m.typ != "HTTP" {
		m.block("a [BODY] condition cannot be checked on a " + strings.ToLower(typeLabel(m.typ)) + " endpoint")
		return
	}
	text, contains := val, false
	if p := gatusPatRe.FindStringSubmatch(val); p != nil {
		inner := p[1]
		if strings.HasPrefix(inner, "*") && strings.HasSuffix(inner, "*") && len(inner) >= 2 && !strings.Contains(inner[1:len(inner)-1], "*") {
			text, contains = inner[1:len(inner)-1], true
		}
		if !contains {
			m.note("The condition "+strconv.Quote(cond)+" was not carried over: only pat(*text*) (the reply must contain the text) maps to a keyword check.", "")
			return
		}
	}
	if cs.keywordSet {
		m.note("The condition "+strconv.Quote(cond)+" was not carried over: a check here can search the reply for one text (keyword is already set).", "")
		return
	}
	m.keyword = text
	cs.keywordSet = true
	if op == "!=" {
		m.absent = true
	} else if !contains {
		m.note("Gatus compares the whole reply body; here the reply is searched for the text instead.", "")
	}
}

// unquote removes one pair of matching quotes around a condition value.
func unquote(s string) string {
	if len(s) >= 2 {
		if q := s[0]; (q == '"' || q == '\'') && s[len(s)-1] == q {
			return s[1 : len(s)-1]
		}
	}
	return s
}
