// Package config reads, validates and edits monitors.conf, the
// text file that holds the whole monitoring setup: SOA defaults with a
// serial number, directives, and records for monitors, nodes, channels,
// policies and status pages.
//
// The parser keeps the original bytes and the line span of every
// statement, so edits made from the Web UI rewrite only the lines they
// touch and comments survive.
package config

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Pos is a position in a configuration file (1-based).
type Pos struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

func (p Pos) String() string {
	if p.File == "" {
		return fmt.Sprintf("%d:%d", p.Line, p.Col)
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// Token is one lexical token.
type Token struct {
	Text string // unescaped text (quotes removed)
	Raw  string // as written
	// Quoted is true when the whole token was a quoted string; such a
	// token is never an option even if it contains '='.
	Quoted bool
	Paren  byte // '(' or ')' for parenthesis tokens, else 0
	Pos    Pos
}

// Stmt is one logical statement: a line, or several lines joined by
// parentheses. StartLine..EndLine (1-based, inclusive) is its span.
type Stmt struct {
	Tokens    []Token
	StartLine int
	EndLine   int
	Comment   string // trailing comment of the last line, without ';'
}

// lexer splits src into statements.
type lexer struct {
	file  string
	src   string
	diags []Diagnostic
}

func (lx *lexer) errorf(pos Pos, code, format string, args ...any) {
	lx.diags = append(lx.diags, Diagnostic{Pos: pos, Severity: SevError, Code: code, Msg: fmt.Sprintf(format, args...)})
}

// statements tokenizes the whole source.
func (lx *lexer) statements() []Stmt {
	var out []Stmt
	var cur *Stmt
	depth := 0
	var parenPos Pos
	lines := strings.Split(lx.src, "\n")
	for i, line := range lines {
		lineNo := i + 1
		line = strings.TrimSuffix(line, "\r")
		if !utf8.ValidString(line) {
			lx.errorf(Pos{lx.file, lineNo, 1}, "invalid_utf8", "line is not valid UTF-8")
			continue
		}
		toks, comment := lx.lineTokens(line, lineNo)
		if cur == nil {
			if len(toks) == 0 {
				continue
			}
			cur = &Stmt{StartLine: lineNo}
		}
		for _, t := range toks {
			switch t.Paren {
			case '(':
				if depth == 0 {
					parenPos = t.Pos
				}
				depth++
			case ')':
				if depth == 0 {
					lx.errorf(t.Pos, "unbalanced_paren", "unexpected ')'")
					continue
				}
				depth--
			}
			cur.Tokens = append(cur.Tokens, t)
		}
		cur.EndLine = lineNo
		cur.Comment = comment
		if depth == 0 {
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		lx.errorf(parenPos, "unbalanced_paren", "'(' is never closed")
		out = append(out, *cur)
	}
	return out
}

// lineTokens tokenizes one physical line.
func (lx *lexer) lineTokens(line string, lineNo int) ([]Token, string) {
	var toks []Token
	col := func(i int) int { return utf8.RuneCountInString(line[:i]) + 1 }
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == ';':
			return toks, strings.TrimSpace(line[i+1:])
		case c == '(' || c == ')':
			toks = append(toks, Token{Text: string(c), Raw: string(c), Paren: c, Pos: Pos{lx.file, lineNo, col(i)}})
			i++
		default:
			start := i
			var text strings.Builder
			quotedWhole := c == '"'
			closed := true
			for i < len(line) {
				c = line[i]
				// A ';' inside a value is literal (anchors=lan:a;wan:b); a
				// comment starts with ';' at a token boundary.
				if c == ' ' || c == '\t' || c == '(' || c == ')' {
					break
				}
				if c != '"' {
					text.WriteByte(c)
					i++
					continue
				}
				// Quoted section, possibly in the middle of a token
				// (key="v a l").
				i++
				closed = false
				for i < len(line) {
					c = line[i]
					if c == '\\' && i+1 < len(line) && (line[i+1] == '"' || line[i+1] == '\\') {
						text.WriteByte(line[i+1])
						i += 2
						continue
					}
					if c == '"' {
						closed = true
						i++
						break
					}
					text.WriteByte(c)
					i++
				}
				if !closed {
					lx.errorf(Pos{lx.file, lineNo, col(start)}, "unterminated_string", "string is never closed")
					break
				}
				if quotedWhole && i < len(line) && line[i] != ' ' && line[i] != '\t' && line[i] != '(' && line[i] != ')' {
					quotedWhole = false
				}
			}
			toks = append(toks, Token{Text: text.String(), Raw: line[start:i], Quoted: quotedWhole, Pos: Pos{lx.file, lineNo, col(start)}})
		}
	}
	return toks, ""
}

// option splits a key=value token. Quoted tokens are never options.
func (t Token) option() (key, val string, ok bool) {
	if t.Quoted || t.Paren != 0 {
		return "", "", false
	}
	k, v, ok := strings.Cut(t.Text, "=")
	// Only a proper option name makes key=value: a URL with a query
	// (https://h/api?token=abc) is a target, quoted or not.
	if !ok || !optKeyRe.MatchString(k) {
		return "", "", false
	}
	return strings.ToLower(k), v, true
}

// quote renders a value for writing back: bare when safe, quoted otherwise.
func quote(v string) string {
	// "=" too: a bare a=b reads back as an option (URLs with a query).
	if v != "" && !strings.ContainsAny(v, " \t;()\"\\=") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(v) + `"`
}
