package config

import (
	"regexp"
	"strings"
)

// File is a parsed configuration file. It keeps the original bytes so
// Bytes() round-trips exactly and edits touch only the lines they change.
type File struct {
	Path  string
	src   []byte
	Stmts []Stmt
	// Renamed records the renames made through RenameRecord (old → new),
	// so that whoever applies the file can carry history over.
	Renamed map[string]string
}

// Opt is one key=value option in source order.
type Opt struct {
	Key   string
	Value string
	Pos   Pos
}

// Record is a parsed record statement.
type Record struct {
	Name     string
	Interval string // as written, "" when omitted
	Type     string // upper case
	Target   string
	TargetOK bool // a target token was present
	Opts     []Opt
	Stmt     *Stmt
	NamePos  Pos
	TypePos  Pos
}

// Directive is a parsed $DIRECTIVE statement.
type Directive struct {
	Name string // upper case, without '$'
	Args []Token
	Stmt *Stmt
}

// Parse tokenizes src. Lexical problems are returned as diagnostics; the
// file is still usable for editing.
func Parse(path string, src []byte) (*File, []Diagnostic) {
	lx := &lexer{file: path, src: string(src)}
	f := &File{Path: path, src: append([]byte(nil), src...)}
	f.Stmts = lx.statements()
	return f, lx.diags
}

// Bytes returns the file content.
func (f *File) Bytes() []byte { return append([]byte(nil), f.src...) }

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// ValidName reports whether s is a valid record name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// isDirective reports whether the statement starts with $.
func (s *Stmt) isDirective() bool {
	return len(s.Tokens) > 0 && !s.Tokens[0].Quoted && strings.HasPrefix(s.Tokens[0].Text, "$")
}

// directive views a statement as a directive.
func (s *Stmt) directive() Directive {
	return Directive{Name: strings.ToUpper(strings.TrimPrefix(s.Tokens[0].Text, "$")), Args: s.Tokens[1:], Stmt: s}
}

// record views a statement as a record; ok is false (with a diagnostic)
// when it does not have the "name [interval] IN TYPE" shape.
func (s *Stmt) record() (Record, *Diagnostic) {
	var toks []Token
	for _, t := range s.Tokens {
		if t.Paren == 0 {
			toks = append(toks, t)
		}
	}
	r := Record{Stmt: s}
	if len(s.Tokens) == 0 {
		return r, &Diagnostic{Pos: Pos{Line: s.StartLine, Col: 1}, Severity: SevError, Code: "syntax", Msg: "empty statement"}
	}
	if len(toks) < 3 {
		return r, &Diagnostic{Pos: s.Tokens[0].Pos, Severity: SevError, Code: "syntax", Msg: `expected "<name> [interval] IN <TYPE> ..."`}
	}
	r.Name, r.NamePos = toks[0].Text, toks[0].Pos
	i := 1
	if !strings.EqualFold(toks[1].Text, "IN") {
		r.Interval = toks[1].Text
		i = 2
	}
	if i >= len(toks) || !strings.EqualFold(toks[i].Text, "IN") {
		p := toks[min(i, len(toks)-1)].Pos
		return r, &Diagnostic{Pos: p, Severity: SevError, Code: "syntax", Msg: `expected "IN" after the record name`}
	}
	i++
	if i >= len(toks) {
		return r, &Diagnostic{Pos: toks[i-1].Pos, Severity: SevError, Code: "syntax", Msg: "missing record type after IN"}
	}
	r.Type, r.TypePos = strings.ToUpper(toks[i].Text), toks[i].Pos
	i++
	rest := toks[i:]
	if len(rest) > 0 {
		if _, _, isOpt := rest[0].option(); !isOpt || r.Type == "SOA" {
			r.Target, r.TargetOK = rest[0].Text, true
			rest = rest[1:]
		}
	}
	for _, t := range rest {
		k, v, ok := t.option()
		if !ok {
			if r.Type == "SOA" {
				r.Opts = append(r.Opts, Opt{Key: "", Value: t.Text, Pos: t.Pos})
				continue
			}
			return r, &Diagnostic{Pos: t.Pos, Severity: SevError, Code: "syntax", Msg: "expected key=value, got " + quote(t.Text)}
		}
		r.Opts = append(r.Opts, Opt{Key: k, Value: v, Pos: t.Pos})
	}
	return r, nil
}

// StmtRecord views a statement as a record (false for directives and
// malformed statements).
func StmtRecord(s *Stmt) (Record, bool) {
	if len(s.Tokens) == 0 || s.isDirective() {
		return Record{}, false
	}
	r, d := s.record()
	return r, d == nil
}
