package importer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A small YAML reader for the subset that tool configurations use: block
// and flow mappings and sequences, plain and quoted scalars, block
// scalars (| and >) and comments. It treats the file strictly as data:
// anchors, aliases, merges, tags, directives and several documents are
// refused rather than interpreted, and nesting and size are bounded.
// Writing it here keeps the program free of a YAML library.

const (
	yamlMaxDepth = 32
	yamlMaxNodes = 20000
)

type ykind int

const (
	yScalar ykind = iota
	yMap
	ySeq
)

type ynode struct {
	kind   ykind
	value  string   // scalar
	quoted bool     // scalar was quoted (always a string)
	keys   []string // map, in order
	vals   []*ynode // map values, same order as keys
	items  []*ynode // sequence
}

func (n *ynode) get(key string) *ynode {
	for i, k := range n.keys {
		if k == key {
			return n.vals[i]
		}
	}
	return nil
}

type yline struct {
	indent int
	text   string // without indentation and comment
	raw    string // the whole line as written (for block scalars)
	no     int
}

type yparser struct {
	lines []yline
	pos   int
	nodes int
}

var errYAML = errors.New("invalid YAML document")

func yerr(no int, msg string) error {
	return errors.New("YAML line " + strconv.Itoa(no) + ": " + msg)
}

// boundedYAML parses one YAML document within the import limits.
func boundedYAML(data []byte) (*ynode, error) {
	if len(data) == 0 || len(data) > AdapterMaxBytes {
		return nil, errors.New("import requires 1 byte to 1 MiB")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("the file is not valid UTF-8")
	}
	p := &yparser{}
	started := false
	for i, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "%") && !started {
			return nil, yerr(i+1, "YAML directives are not supported")
		}
		if trim == "---" || strings.HasPrefix(trim, "--- ") {
			if started || len(p.lines) > 0 {
				return nil, errors.New("exactly one document is required")
			}
			started = true
			continue
		}
		if trim == "..." {
			return nil, errors.New("exactly one document is required")
		}
		ind := len(raw) - len(strings.TrimLeft(raw, " "))
		if strings.HasPrefix(raw[ind:], "\t") {
			return nil, yerr(i+1, "tabs cannot be used for indentation")
		}
		p.lines = append(p.lines, yline{indent: ind, text: stripComment(raw[ind:]), raw: raw, no: i + 1})
	}
	p.skipBlank()
	if p.pos >= len(p.lines) {
		return nil, errYAML
	}
	root, err := p.block(p.lines[p.pos].indent, 0)
	if err != nil {
		return nil, err
	}
	p.skipBlank()
	if p.pos < len(p.lines) {
		return nil, yerr(p.lines[p.pos].no, "unexpected content")
	}
	return root, nil
}

// stripComment removes a trailing comment outside quotes.
func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == '\\' && q == '"' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			if i == 0 || strings.ContainsRune(" \t[{,:-", rune(s[i-1])) {
				q = c
			}
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimRight(s[:i], " \t")
		}
	}
	return strings.TrimRight(s, " \t")
}

func (p *yparser) skipBlank() {
	for p.pos < len(p.lines) && p.lines[p.pos].text == "" {
		p.pos++
	}
}

func (p *yparser) count(depth int) error {
	p.nodes++
	if depth > yamlMaxDepth || p.nodes > yamlMaxNodes {
		return errors.New("import nesting or node limit exceeded")
	}
	return nil
}

func isSeqItem(t string) bool { return t == "-" || strings.HasPrefix(t, "- ") }

// block parses the mapping or sequence that starts at the current line.
func (p *yparser) block(indent, depth int) (*ynode, error) {
	if err := p.count(depth); err != nil {
		return nil, err
	}
	l := p.lines[p.pos]
	if isSeqItem(l.text) {
		return p.seq(indent, depth)
	}
	if _, _, ok := splitKey(l.text); ok {
		return p.mapping(indent, depth)
	}
	// A lone scalar document or continuation.
	p.pos++
	return p.scalar(l.text, l.no, depth)
}

func (p *yparser) seq(indent, depth int) (*ynode, error) {
	n := &ynode{kind: ySeq}
	for {
		p.skipBlank()
		if p.pos >= len(p.lines) {
			break
		}
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent || !isSeqItem(l.text) {
			return nil, yerr(l.no, "bad indentation in a list")
		}
		rest := strings.TrimLeft(strings.TrimPrefix(l.text, "-"), " ")
		var item *ynode
		var err error
		switch {
		case rest == "":
			p.pos++
			item, err = p.child(indent, depth)
		case !strings.HasPrefix(rest, "{") && !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, "\"") && !strings.HasPrefix(rest, "'") && hasKey(rest):
			// "- key: value" starts a mapping indented to the key.
			inner := indent + len(l.text) - len(rest)
			p.lines[p.pos] = yline{indent: inner, text: rest, raw: l.raw, no: l.no}
			item, err = p.mapping(inner, depth+1)
		default:
			p.pos++
			item, err = p.scalar(rest, l.no, depth+1)
		}
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, item)
	}
	return n, nil
}

func hasKey(s string) bool { _, _, ok := splitKey(s); return ok }

// child parses the value on the following, more indented lines (or an
// empty value when there are none).
func (p *yparser) child(indent, depth int) (*ynode, error) {
	p.skipBlank()
	if p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent > indent || (l.indent == indent && isSeqItem(l.text)) {
			return p.block(l.indent, depth+1)
		}
	}
	return &ynode{kind: yScalar}, nil
}

func (p *yparser) mapping(indent, depth int) (*ynode, error) {
	n := &ynode{kind: yMap}
	for {
		p.skipBlank()
		if p.pos >= len(p.lines) {
			break
		}
		l := p.lines[p.pos]
		if l.indent < indent || (l.indent == indent && isSeqItem(l.text)) {
			break
		}
		if l.indent > indent {
			return nil, yerr(l.no, "bad indentation")
		}
		key, rest, ok := splitKey(l.text)
		if !ok {
			return nil, yerr(l.no, "expected key: value")
		}
		k, err := keyText(key, l.no)
		if err != nil {
			return nil, err
		}
		if n.get(k) != nil {
			return nil, yerr(l.no, "duplicate or non-string mapping key")
		}
		var v *ynode
		p.pos++
		switch {
		case rest == "":
			v, err = p.child(indent, depth)
		case rest[0] == '|' || rest[0] == '>':
			v, err = p.blockScalar(rest, indent, l.no)
		default:
			v, err = p.scalar(rest, l.no, depth+1)
		}
		if err != nil {
			return nil, err
		}
		n.keys = append(n.keys, k)
		n.vals = append(n.vals, v)
	}
	return n, nil
}

// splitKey splits "key: value" at the first ": " (or a trailing ":")
// outside quotes and brackets.
func splitKey(s string) (key, rest string, ok bool) {
	var q byte
	level := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == '\\' && q == '"' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '[' || c == '{':
			level++
		case c == ']' || c == '}':
			level--
		case c == ':' && level == 0 && (i == len(s)-1 || s[i+1] == ' '):
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

func keyText(k string, no int) (string, error) {
	if k == "" || k == "<<" || k[0] == '?' {
		return "", yerr(no, "duplicate or non-string mapping key")
	}
	n, err := plainOrQuoted(k, no)
	if err != nil {
		return "", err
	}
	return n.value, nil
}

func (p *yparser) blockScalar(head string, indent, no int) (*ynode, error) {
	if len(head) > 2 || (len(head) == 2 && head[1] != '-' && head[1] != '+') {
		return nil, yerr(no, "unsupported block scalar header")
	}
	var lines []string
	min := -1
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if strings.TrimSpace(l.raw) != "" && l.indent <= indent {
			break
		}
		if strings.TrimSpace(l.raw) != "" && (min < 0 || l.indent < min) {
			min = l.indent
		}
		lines = append(lines, l.raw)
		p.pos++
	}
	for i, s := range lines {
		if len(s) >= min && min > 0 {
			lines[i] = s[min:]
		} else {
			lines[i] = strings.TrimLeft(s, " ")
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	sep := "\n"
	if head[0] == '>' {
		sep = " "
	}
	v := strings.Join(lines, sep)
	if !strings.HasSuffix(head, "-") && v != "" {
		v += "\n"
	}
	return &ynode{kind: yScalar, value: v, quoted: true}, nil
}

// scalar parses an inline value: a flow collection or a scalar.
func (p *yparser) scalar(s string, no, depth int) (*ynode, error) {
	if err := p.count(depth); err != nil {
		return nil, err
	}
	if s != "" && (s[0] == '[' || s[0] == '{') {
		f := &flow{s: s, no: no, p: p}
		n, err := f.value(depth)
		if err != nil {
			return nil, err
		}
		f.space()
		if f.i != len(f.s) {
			return nil, yerr(no, "unexpected text after a flow collection")
		}
		return n, nil
	}
	return plainOrQuoted(s, no)
}

func plainOrQuoted(s string, no int) (*ynode, error) {
	if s == "" {
		return &ynode{kind: yScalar}, nil
	}
	switch s[0] {
	case '&', '*':
		return nil, yerr(no, "YAML anchors, aliases and merges are not supported")
	case '!':
		return nil, yerr(no, "custom YAML tags are not supported")
	case '%', '@', '`', '|', '>':
		return nil, yerr(no, "unsupported value")
	case '"':
		if len(s) < 2 || s[len(s)-1] != '"' {
			return nil, yerr(no, "unterminated string")
		}
		v, err := strconv.Unquote(s)
		if err != nil {
			// YAML allows a few escapes Go does not; refuse rather than guess.
			return nil, yerr(no, "unsupported escape in a string")
		}
		return &ynode{kind: yScalar, value: v, quoted: true}, nil
	case '\'':
		if len(s) < 2 || s[len(s)-1] != '\'' {
			return nil, yerr(no, "unterminated string")
		}
		inner := s[1 : len(s)-1]
		if strings.Contains(strings.ReplaceAll(inner, "''", ""), "'") {
			return nil, yerr(no, "unterminated string")
		}
		return &ynode{kind: yScalar, value: strings.ReplaceAll(inner, "''", "'"), quoted: true}, nil
	}
	return &ynode{kind: yScalar, value: s}, nil
}

// flow parses [a, b] and {k: v} collections on one line.
type flow struct {
	s  string
	i  int
	no int
	p  *yparser
}

func (f *flow) space() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

func (f *flow) value(depth int) (*ynode, error) {
	if err := f.p.count(depth); err != nil {
		return nil, err
	}
	f.space()
	if f.i >= len(f.s) {
		return nil, yerr(f.no, "unterminated flow collection")
	}
	switch f.s[f.i] {
	case '[':
		f.i++
		n := &ynode{kind: ySeq}
		for {
			f.space()
			if f.i < len(f.s) && f.s[f.i] == ']' {
				f.i++
				return n, nil
			}
			v, err := f.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, v)
			if err := f.sep(']'); err != nil {
				return nil, err
			}
		}
	case '{':
		f.i++
		n := &ynode{kind: yMap}
		for {
			f.space()
			if f.i < len(f.s) && f.s[f.i] == '}' {
				f.i++
				return n, nil
			}
			k, err := f.atom(true)
			if err != nil {
				return nil, err
			}
			if k.kind != yScalar || k.value == "" || k.value == "<<" || n.get(k.value) != nil {
				return nil, yerr(f.no, "duplicate or non-string mapping key")
			}
			f.space()
			if f.i >= len(f.s) || f.s[f.i] != ':' {
				return nil, yerr(f.no, "expected : in a flow mapping")
			}
			f.i++
			v, err := f.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, k.value)
			n.vals = append(n.vals, v)
			if err := f.sep('}'); err != nil {
				return nil, err
			}
		}
	}
	return f.atom(false)
}

func (f *flow) sep(end byte) error {
	f.space()
	if f.i < len(f.s) && f.s[f.i] == ',' {
		f.i++
		return nil
	}
	if f.i < len(f.s) && f.s[f.i] == end {
		return nil
	}
	return yerr(f.no, "expected , or closing bracket")
}

// atom reads one scalar inside a flow collection.
func (f *flow) atom(key bool) (*ynode, error) {
	f.space()
	start := f.i
	if f.i < len(f.s) && (f.s[f.i] == '"' || f.s[f.i] == '\'') {
		q := f.s[f.i]
		f.i++
		for f.i < len(f.s) {
			if q == '"' && f.s[f.i] == '\\' {
				f.i += 2
				continue
			}
			if f.s[f.i] == q {
				if q == '\'' && f.i+1 < len(f.s) && f.s[f.i+1] == '\'' {
					f.i += 2
					continue
				}
				break
			}
			f.i++
		}
		if f.i >= len(f.s) {
			return nil, yerr(f.no, "unterminated string")
		}
		f.i++
		return plainOrQuoted(f.s[start:f.i], f.no)
	}
	for f.i < len(f.s) {
		c := f.s[f.i]
		if c == ',' || c == ']' || c == '}' || c == '[' || c == '{' || (key && c == ':') || (c == ':' && f.i+1 < len(f.s) && f.s[f.i+1] == ' ') {
			break
		}
		f.i++
	}
	if f.i < len(f.s) && (f.s[f.i] == '[' || f.s[f.i] == '{') {
		return nil, yerr(f.no, "unexpected bracket")
	}
	return plainOrQuoted(strings.TrimSpace(f.s[start:f.i]), f.no)
}

// boundedJSON checks a JSON document for one value, nesting depth and
// duplicate object keys before it is decoded.
func boundedJSON(data []byte) error {
	if len(data) == 0 || len(data) > AdapterMaxBytes || !utf8.Valid(data) || !json.Valid(data) {
		return errors.New("invalid JSON or larger than 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	type frame struct {
		obj  bool
		key  bool
		seen map[string]bool
	}
	var stack []*frame
	nodes := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid JSON")
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.obj && top.key {
			if s, ok := tok.(string); ok {
				if top.seen[s] {
					return errors.New("duplicate or non-string mapping key")
				}
				top.seen[s] = true
				top.key = false
				continue
			}
		}
		nodes++
		if nodes > yamlMaxNodes {
			return errors.New("import nesting or node limit exceeded")
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			stack = append(stack, &frame{obj: tok == json.Delim('{'), key: true, seen: map[string]bool{}})
			if len(stack) > yamlMaxDepth {
				return errors.New("import nesting or node limit exceeded")
			}
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 && stack[len(stack)-1].obj {
			stack[len(stack)-1].key = true
		}
	}
	return nil
}
