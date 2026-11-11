package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Selector picks monitors by fields that depend only on the configuration,
// so the same file always selects the same monitors. Terms are separated by
// spaces and must all match; a term is key:value (commas mean "any of"),
// or plain text matched against name, title, target, description and
// tags. A leading '-' negates a term. A value written /like this/ is a
// case-insensitive regular expression.
//
// Keys: name, title, type, svc, port, host, device, cidr, tag, group,
// policy, node, area.
type Selector struct {
	terms []selTerm
	src   string
}

type selTerm struct {
	key  string
	vals []selVal
	neg  bool
}

type selVal struct {
	text string
	re   *regexp.Regexp
	cidr netip.Prefix
}

var selectorKeys = map[string]bool{
	"name": true, "title": true, "type": true, "svc": true, "port": true, "host": true, "cidr": true,
	"tag": true, "group": true, "policy": true, "node": true, "area": true, "device": true, "team": true, "region": true,
}

// ParseSelector parses a selector; "" selects nothing.
func ParseSelector(s string) (*Selector, error) {
	sel := &Selector{src: s}
	for _, f := range splitSelector(s) {
		t := selTerm{}
		if strings.HasPrefix(f, "-") && len(f) > 1 {
			t.neg, f = true, f[1:]
		}
		key, val, ok := strings.Cut(f, ":")
		if ok && !selectorKeys[strings.ToLower(key)] {
			if strings.Contains("status flag latency uptime changed acked paused", strings.ToLower(key)) {
				return nil, fmt.Errorf("%s changes at run time and cannot select monitors here", key)
			}
			ok = false // "a:b" text, e.g. part of a URL
		}
		if !ok {
			key, val = "", f
		}
		t.key = strings.ToLower(key)
		parts := []string{val}
		if t.key != "" && !strings.HasPrefix(val, "/") {
			parts = strings.Split(val, ",")
		}
		for _, p := range parts {
			v := selVal{text: strings.ToLower(p)}
			if len(p) > 1 && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") {
				re, err := regexp.Compile("(?i)" + p[1:len(p)-1])
				if err != nil {
					return nil, fmt.Errorf("%s: %v", p, err)
				}
				v.re = re
			}
			if t.key == "cidr" {
				pf, err := netip.ParsePrefix(p)
				if err != nil {
					return nil, fmt.Errorf("cidr %q: %v", p, err)
				}
				v.cidr = pf
			}
			if t.key == "port" {
				if _, err := strconv.Atoi(p); err != nil {
					return nil, fmt.Errorf("port %q is not a number", p)
				}
			}
			t.vals = append(t.vals, v)
		}
		sel.terms = append(sel.terms, t)
	}
	return sel, nil
}

// splitSelector splits on spaces outside double quotes (a regular
// expression spells a space as \s).
func splitSelector(s string) []string {
	var out []string
	var b strings.Builder
	quote := false
	for _, r := range s {
		switch {
		case r == '"':
			quote = !quote
			continue
		case (r == ' ' || r == '\t') && !quote:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// String returns the selector as written.
func (s *Selector) String() string { return s.src }

// Empty reports whether the selector has no terms (it selects nothing).
func (s *Selector) Empty() bool { return s == nil || len(s.terms) == 0 }

// Match reports whether m is selected.
func (s *Selector) Match(m *Monitor) bool {
	if s.Empty() {
		return false
	}
	for _, t := range s.terms {
		if t.match(m) == t.neg {
			return false
		}
	}
	return true
}

func (t selTerm) match(m *Monitor) bool {
	for _, v := range t.vals {
		if t.matchOne(m, v) {
			return true
		}
	}
	return false
}

func (v selVal) is(s string) bool {
	if v.re != nil {
		return v.re.MatchString(s)
	}
	return strings.EqualFold(s, v.text)
}

func (v selVal) contains(s string) bool {
	if v.re != nil {
		return v.re.MatchString(s)
	}
	return strings.Contains(strings.ToLower(s), v.text)
}

// selFields are the terms that compare one field of the monitor exactly.
var selFields = map[string]func(*Monitor) string{
	"name": func(m *Monitor) string { return m.Name }, "host": (*Monitor).Host, "device": (*Monitor).HostKey,
	"group": func(m *Monitor) string { return m.Group }, "policy": func(m *Monitor) string { return m.Policy },
	"region": func(m *Monitor) string { return m.Region }, "area": func(m *Monitor) string { return m.Area },
}

func (t selTerm) matchOne(m *Monitor, v selVal) bool {
	if field, ok := selFields[t.key]; ok {
		return v.is(field(m))
	}
	switch t.key {
	case "title":
		return v.contains(m.Title)
	case "type":
		return v.is(m.Type) || v.is(m.Written)
	case "svc":
		return m.UDP != nil && v.is(m.UDP.Keyword)
	case "port":
		return strconv.Itoa(m.Port()) == v.text
	case "cidr":
		a, err := netip.ParseAddr(strings.Trim(m.Host(), "[]"))
		return err == nil && v.cidr.Contains(a.Unmap())
	case "team":
		return v.is(m.Team) || v.is(m.TeamOf())
	case "node":
		return matchNode(m, v)
	case "tag":
		return matchTag(m, v)
	}
	return matchText(m, v)
}

func matchNode(m *Monitor, v selVal) bool {
	if len(m.Nodes) == 0 {
		return true // checked by every node
	}
	for _, n := range m.Nodes {
		if v.is(n) {
			return true
		}
	}
	return false
}

func matchTag(m *Monitor, v selVal) bool {
	key, val, hasVal := strings.Cut(v.text, ":")
	if !hasVal {
		key, val, hasVal = strings.Cut(v.text, "=")
	}
	for _, tg := range m.Tags {
		if strings.EqualFold(tg.Key, key) && (!hasVal || strings.EqualFold(tg.Value, val)) {
			return true
		}
	}
	return false
}

// matchText is free text: the name, title, target, description or a tag.
func matchText(m *Monitor, v selVal) bool {
	for _, s := range []string{m.Name, m.Title, m.Target, m.Description} {
		if v.contains(s) {
			return true
		}
	}
	for _, tg := range m.Tags {
		if v.contains(tg.String()) {
			return true
		}
	}
	return false
}

// Host is the host a monitor checks: the URL host, the host of host:port,
// or the target itself. Push monitors have none.
func (m *Monitor) Host() string {
	if m.UDP != nil {
		return m.UDP.Host
	}
	switch m.Type {
	case "PUSH":
		return ""
	case "EXEC", "DOCKER":
		return m.Opts["address"] // the target is a plugin or a container
	case "HTTP":
		if u, err := url.Parse(m.Target); err == nil {
			return u.Hostname()
		}
	case "TCP":
		if h, _, err := net.SplitHostPort(m.Target); err == nil {
			return h
		}
	}
	return strings.Trim(m.Target, "[]")
}

// HostKey is the host a monitor is listed under: device= when set, else
// the host it checks, else (push monitors) its own name.
func (m *Monitor) HostKey() string {
	if m.Device != "" {
		return m.Device
	}
	if h := m.Host(); h != "" {
		return strings.ToLower(h)
	}
	return m.Name
}

// Port is the port a monitor checks, 0 when it has none.
func (m *Monitor) Port() int {
	if m.UDP != nil {
		return m.UDP.Port
	}
	switch m.Type {
	case "HTTP":
		u, err := url.Parse(m.Target)
		if err != nil {
			return 0
		}
		if p, err := strconv.Atoi(u.Port()); err == nil {
			return p
		}
		if u.Scheme == "https" {
			return 443
		}
		return 80
	case "TCP":
		if _, p, err := net.SplitHostPort(m.Target); err == nil {
			n, _ := strconv.Atoi(p)
			return n
		}
	}
	return 0
}
