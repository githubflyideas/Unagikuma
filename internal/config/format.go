package config

import (
	"strings"
	"unicode/utf8"
)

// Format returns the file in the canonical layout `unagikuma fmt` writes:
// single-line records are re-spaced with their name, interval, IN and type
// in columns shared by each block of consecutive records, values quoted
// only when they must be and option names in lower case. Comments,
// directives, the SOA and records written over several lines are left as
// they are; the meaning never changes. Formatting twice changes nothing.
func Format(src []byte) []byte {
	f, diags := Parse("", src)
	if HasErrors(diags) {
		return src
	}
	text := string(src)
	cr := ""
	if strings.Contains(text, "\r\n") {
		cr = "\r"
	}
	lines := strings.Split(text, "\n")
	type rec struct {
		line    int
		r       Record
		comment string
	}
	byLine := map[int]rec{}
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if s.StartLine != s.EndLine || s.isDirective() {
			continue
		}
		r, ok := StmtRecord(s)
		if !ok || r.Type == "SOA" {
			continue
		}
		for _, t := range s.Tokens {
			if t.Paren != 0 {
				r.Type = "" // parentheses on one line: leave it
			}
		}
		if r.Type != "" {
			byLine[s.StartLine-1] = rec{s.StartLine - 1, r, s.Comment}
		}
	}
	width := func(s string) int { return utf8.RuneCountInString(s) }
	for i := 0; i < len(lines); {
		if _, ok := byLine[i]; !ok {
			i++
			continue
		}
		// A block of consecutive one-line records of one family
		// (monitors, channels, policies...).
		j := i
		nameW, ivW, typeW := 0, 0, 0
		for ; j < len(lines); j++ {
			r, ok := byLine[j]
			if !ok || !sameFamily(r.r.Type, byLine[i].r.Type) {
				break
			}
			nameW = max(nameW, width(r.r.Name))
			ivW = max(ivW, width(r.r.Interval))
			typeW = max(typeW, width(r.r.Type))
		}
		for k := i; k < j; k++ {
			r := byLine[k].r
			var b strings.Builder
			b.WriteString(r.Name + strings.Repeat(" ", nameW-width(r.Name)+1))
			if ivW > 0 {
				b.WriteString(strings.Repeat(" ", ivW-width(r.Interval)) + r.Interval + " ")
			}
			b.WriteString("IN " + r.Type)
			if r.TargetOK || len(r.Opts) > 0 || byLine[k].comment != "" {
				b.WriteString(strings.Repeat(" ", typeW-width(r.Type)))
			}
			if r.TargetOK {
				b.WriteString(" " + quote(r.Target))
			}
			for _, o := range r.Opts {
				b.WriteString(" " + o.Key + "=" + quote(o.Value))
			}
			if c := byLine[k].comment; c != "" {
				b.WriteString(" ; " + c)
			}
			lines[k] = strings.TrimRight(b.String(), " ") + cr
		}
		i = j
	}
	return []byte(strings.Join(lines, "\n"))
}
