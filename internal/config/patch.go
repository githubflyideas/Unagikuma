package config

import (
	"sort"
	"strings"
)

// Writing back an existing record changes only what changed: the
// options keep their order, quoting and the column alignment of the
// line; removed options disappear, new ones are added at the end. A file
// kept in git then shows a one-value diff for a one-value change.

type lineEdit struct {
	line       int // 0-based
	start, end int // byte offsets in the line
	text       string
}

// patchRecord rewrites the statement of an existing record into r.
func (f *File) patchRecord(s *Stmt, old Record, r RecordSpec) []string {
	p := &recordPatch{ls: f.lines()}
	var toks []Token
	for _, t := range s.Tokens {
		if t.Paren == 0 {
			toks = append(toks, t)
		}
	}
	i, last := p.head(toks, old, r)
	p.options(toks[i:], last, r)
	// Apply right to left within each line.
	sort.Slice(p.edits, func(a, b int) bool {
		if p.edits[a].line != p.edits[b].line {
			return p.edits[a].line < p.edits[b].line
		}
		return p.edits[a].start > p.edits[b].start
	})
	out := append([]string(nil), p.ls...)
	for _, e := range p.edits {
		line := out[e.line]
		out[e.line] = line[:e.start] + e.text + line[e.end:]
	}
	return out
}

// recordPatch collects the edits that turn one statement into another.
type recordPatch struct {
	ls    []string
	edits []lineEdit
}

func (p *recordPatch) span(t Token) (int, int, int) {
	ln := t.Pos.Line - 1
	start := byteIndexOfCol(p.ls[ln], t.Pos.Col)
	return ln, start, start + len(t.Raw)
}

// replace swaps a token; aligned keeps the next token's column when the
// gap after it is wider than one space (columns lined up by hand).
func (p *recordPatch) replace(t Token, text string, aligned bool) {
	if t.Raw == text {
		return
	}
	ln, a, b := p.span(t)
	line := p.ls[ln]
	if aligned {
		w := 0
		for b+w < len(line) && line[b+w] == ' ' {
			w++
		}
		if w > 1 && b+w < len(line) {
			gap := max(1, w-(len(text)-len(t.Raw)))
			p.edits = append(p.edits, lineEdit{ln, a, b + w, text + strings.Repeat(" ", gap)})
			return
		}
	}
	p.edits = append(p.edits, lineEdit{ln, a, b, text})
}

func (p *recordPatch) remove(t Token) {
	ln, a, b := p.span(t)
	line := p.ls[ln]
	for a > 0 && (line[a-1] == ' ' || line[a-1] == '\t') {
		a--
	}
	if a == 0 { // first on its line: take the space after it instead
		for b < len(line) && (line[b] == ' ' || line[b] == '\t') {
			b++
		}
	}
	p.edits = append(p.edits, lineEdit{ln, a, b, ""})
}

func (p *recordPatch) insertAfter(t Token, text string) {
	ln, _, b := p.span(t)
	p.edits = append(p.edits, lineEdit{ln, b, b, text})
}

// head patches name, interval, type and target; it returns the index of
// the first option token and the last token kept.
func (p *recordPatch) head(toks []Token, old Record, r RecordSpec) (int, Token) {
	i := 0
	nameTok := toks[i]
	p.replace(nameTok, r.Name, true)
	i++
	if old.Interval != "" {
		if r.Interval == "" {
			p.remove(toks[i])
		} else {
			p.replace(toks[i], r.Interval, true)
		}
		i++
	} else if r.Interval != "" {
		p.insertAfter(nameTok, " "+r.Interval)
	}
	i++ // IN
	typeTok := toks[i]
	if !strings.EqualFold(typeTok.Text, r.Type) {
		p.replace(typeTok, strings.ToUpper(r.Type), true)
	}
	last := typeTok
	i++
	if old.TargetOK {
		t := toks[i]
		switch {
		case r.Target == "":
			p.remove(t)
		case r.Target != old.Target:
			p.replace(t, quoteLike(t.Raw, r.Target), false)
		}
		last = t
		i++
	} else if r.Target != "" {
		p.insertAfter(typeTok, " "+quote(r.Target))
	}
	return i, last
}

// options changes values in place, removes options that are gone and adds
// new ones after the last token.
func (p *recordPatch) options(toks []Token, last Token, r RecordSpec) {
	want := map[string][]string{}
	var order []string
	for _, o := range r.Opts {
		if _, seen := want[o.Key]; !seen {
			order = append(order, o.Key)
		}
		want[o.Key] = append(want[o.Key], o.Value)
	}
	for _, t := range toks {
		last = t
		k, v, ok := t.option()
		if !ok {
			continue
		}
		vals := want[k]
		if len(vals) == 0 {
			p.remove(t)
			continue
		}
		want[k] = vals[1:]
		if vals[0] != v {
			_, rawVal, _ := strings.Cut(t.Raw, "=")
			p.replace(t, rawKey(t.Raw)+"="+quoteLike(rawVal, vals[0]), false)
		}
	}
	var add strings.Builder
	for _, k := range order {
		for _, v := range want[k] {
			add.WriteString(" " + k + "=" + quote(v))
		}
	}
	if add.Len() > 0 {
		p.insertAfter(last, add.String())
	}
}

// rawKey is the option name as written (its case kept).
func rawKey(raw string) string {
	k, _, _ := strings.Cut(raw, "=")
	return k
}

// quoteLike writes v quoted when the original was, else only when needed.
func quoteLike(origRaw, v string) string {
	if strings.HasPrefix(origRaw, `"`) {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
		return `"` + r.Replace(v) + `"`
	}
	return quote(v)
}
