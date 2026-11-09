package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Edits rewrite only the lines of the statements they touch, so comments,
// alignment and blank lines elsewhere survive a round trip through the
// Web UI. Every edit re-parses the file to keep line spans current.

func (f *File) lines() []string { return strings.Split(string(f.src), "\n") }

func (f *File) setLines(ls []string) error {
	nf, diags := Parse(f.Path, []byte(strings.Join(ls, "\n")))
	if HasErrors(diags) {
		return fmt.Errorf("edit produced an invalid file: %v", diags[0])
	}
	renamed := f.Renamed
	*f = *nf
	f.Renamed = renamed
	return nil
}

// eol returns the line ending used by the file.
func (f *File) eol() string {
	if strings.Contains(string(f.src), "\r\n") {
		return "\r"
	}
	return ""
}

// serialToken finds the token that holds the revision: revision=N on the
// $FILE line (prefix "revision="), or the serial of an SOA record.
func (f *File) serialToken() (t *Token, prefix string, err error) {
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if s.isDirective() {
			d := s.directive()
			if d.Name != "FILE" {
				continue
			}
			for j := 1; j < len(s.Tokens); j++ {
				t := &s.Tokens[j]
				if k, _, ok := t.option(); ok && k == "revision" {
					key, _, _ := strings.Cut(t.Raw, "=")
					return t, key + "=", nil
				}
			}
			return nil, "", errors.New("$FILE has no revision")
		}
		r, d := s.record()
		if d != nil || r.Type != "SOA" {
			continue
		}
		n := 0
		for j := range s.Tokens {
			t := &s.Tokens[j]
			if t.Paren != 0 {
				continue
			}
			// name IN SOA mname rname serial ...
			if n == 5 {
				return t, "", nil
			}
			n++
		}
		return nil, "", errors.New("SOA has no serial")
	}
	return nil, "", errors.New("no file header")
}

// Serial returns the revision written in the file.
func (f *File) Serial() (uint32, error) {
	t, prefix, err := f.serialToken()
	if err != nil {
		return 0, err
	}
	v := t.Text
	if prefix != "" {
		_, v, _ = t.option()
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid revision %q", v)
	}
	return uint32(n), nil
}

// SetSerial replaces the revision in place, keeping the rest of the line.
func (f *File) SetSerial(n uint32) error {
	t, prefix, err := f.serialToken()
	if err != nil {
		return err
	}
	ls := f.lines()
	line := ls[t.Pos.Line-1]
	idx := byteIndexOfCol(line, t.Pos.Col)
	ls[t.Pos.Line-1] = line[:idx] + prefix + strconv.FormatUint(uint64(n), 10) + line[idx+len(t.Raw):]
	return f.setLines(ls)
}

func byteIndexOfCol(line string, col int) int {
	c := 1
	for i := range line {
		if c == col {
			return i
		}
		c++
	}
	return len(line)
}

// SetMaster sets the master node name on the $FILE line (master=) or the
// SOA mname. A $FILE without master= gets the option added.
func (f *File) SetMaster(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid master name %q", name)
	}
	ls := f.lines()
	cr := f.eol()
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if s.isDirective() {
			d := s.directive()
			if d.Name != "FILE" {
				continue
			}
			for _, t := range d.Args {
				k, _, ok := t.option()
				if !ok || k != "master" {
					continue
				}
				line := ls[t.Pos.Line-1]
				idx := byteIndexOfCol(line, t.Pos.Col)
				key, _, _ := strings.Cut(t.Raw, "=")
				ls[t.Pos.Line-1] = line[:idx] + key + "=" + name + line[idx+len(t.Raw):]
				return f.setLines(ls)
			}
			// No master= yet: append it to the $FILE line.
			line := strings.TrimRight(ls[s.StartLine-1], "\r\n")
			ls[s.StartLine-1] = line + " master=" + name + cr
			return f.setLines(ls)
		}
		r, d := s.record()
		if d != nil || r.Type != "SOA" {
			continue
		}
		// name IN SOA mname rname ... — mname is the 4th bare token.
		n := 0
		for j := range s.Tokens {
			t := &s.Tokens[j]
			if t.Paren != 0 {
				continue
			}
			if n == 3 {
				line := ls[t.Pos.Line-1]
				idx := byteIndexOfCol(line, t.Pos.Col)
				repl := name
				if strings.HasSuffix(t.Raw, ".") {
					repl += "."
				}
				ls[t.Pos.Line-1] = line[:idx] + repl + line[idx+len(t.Raw):]
				return f.setLines(ls)
			}
			n++
		}
		return errors.New("SOA has no mname")
	}
	return errors.New("no file header")
}

// RecordSpec is a record to write: Opts in the given order.
type RecordSpec struct {
	Name     string
	Interval string
	Type     string
	Target   string
	Opts     []Opt
	Comment  string
	// Area places a new monitor under the $SCOPE with this spec ("" =
	// the global section). An existing record stays where it is unless
	// MoveArea is set.
	Area     string
	MoveArea bool
}

var (
	typeRe     = regexp.MustCompile(`^[A-Za-z]{1,16}$`)
	intervalRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.]{0,15}$`)
	optKeyRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
)

// check refuses anything that would not stay one record on one line:
// values come from the API, joining machines and imported files, and a
// line break would let them write other records.
func (r RecordSpec) check() error {
	if !ValidName(r.Name) {
		return fmt.Errorf("invalid name %q", r.Name)
	}
	if !typeRe.MatchString(r.Type) {
		return fmt.Errorf("invalid record type %q", r.Type)
	}
	if r.Interval != "" && !intervalRe.MatchString(r.Interval) {
		return fmt.Errorf("invalid interval %q", r.Interval)
	}
	if err := oneLine("target", r.Target); err != nil {
		return err
	}
	if err := oneLine("comment", r.Comment); err != nil {
		return err
	}
	if strings.ContainsAny(r.Area, " \t;\"\\") || oneLine("section", r.Area) != nil {
		return fmt.Errorf("invalid section %q", r.Area)
	}
	for _, o := range r.Opts {
		if !optKeyRe.MatchString(o.Key) {
			return fmt.Errorf("invalid option name %q", o.Key)
		}
		if err := oneLine(o.Key, o.Value); err != nil {
			return err
		}
	}
	return nil
}

// oneLine rejects control characters (tabs aside).
func oneLine(what, v string) error {
	for _, c := range v {
		if (c < 0x20 && c != '\t') || c == 0x7f || c == 0x2028 || c == 0x2029 {
			return fmt.Errorf("%s contains a line break or control character", what)
		}
	}
	return nil
}

// Render formats a record on one line.
func (r RecordSpec) Render() string {
	var b strings.Builder
	b.WriteString(r.Name)
	if r.Interval != "" {
		b.WriteString(" " + r.Interval)
	}
	b.WriteString(" IN " + strings.ToUpper(r.Type))
	if r.Target != "" {
		b.WriteString(" " + quote(r.Target))
	}
	for _, o := range r.Opts {
		b.WriteString(" " + o.Key + "=" + quote(o.Value))
	}
	if r.Comment != "" {
		b.WriteString(" ; " + r.Comment)
	}
	return b.String()
}

// Record returns a named record as written.
func (f *File) Record(name string) (Record, bool) {
	s, r := f.findRecord(name)
	if s == nil {
		return Record{}, false
	}
	return *r, true
}

// findRecord returns the statement of a named record.
func (f *File) findRecord(name string) (*Stmt, *Record) {
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if s.isDirective() {
			continue
		}
		if r, d := s.record(); d == nil && r.Name == name && r.Type != "SOA" {
			return s, &r
		}
	}
	return nil, nil
}

// UpsertRecord replaces a record in place (keeping its trailing comment
// when the new one has none), or appends it after the last record of the
// same type, or at the end of the file.
// ErrNameTaken: the name is used by a record of another kind. Names are
// unique across all record types, and saving a channel, a policy or a team
// must never replace a monitor (or the other way round) of the same name.
var ErrNameTaken = errors.New("the name is already used by another kind of record")

func (f *File) UpsertRecord(r RecordSpec) error {
	if err := r.check(); err != nil {
		return err
	}
	if _, old := f.findRecord(r.Name); old != nil {
		same := strings.EqualFold(old.Type, r.Type) || IsMonitorType(strings.ToUpper(old.Type)) && IsMonitorType(strings.ToUpper(r.Type))
		if !same {
			return fmt.Errorf("%w: %s is a %s", ErrNameTaken, r.Name, strings.ToUpper(old.Type))
		}
	}
	ls := f.lines()
	cr := f.eol()
	if s, _ := f.findRecord(r.Name); s != nil && r.MoveArea && IsMonitorType(r.Type) && f.areaOf(s) != r.Area {
		if r.Comment == "" {
			r.Comment = s.Comment
		}
		if err := f.DeleteRecord(r.Name); err != nil {
			return err
		}
		r.MoveArea = false
		return f.UpsertRecord(r)
	}
	if s, old := f.findRecord(r.Name); s != nil {
		if r.Comment == "" || r.Comment == s.Comment {
			return f.setLines(f.patchRecord(s, *old, r))
		}
		out := append([]string{}, ls[:s.StartLine-1]...)
		out = append(out, r.Render()+cr)
		out = append(out, ls[s.EndLine:]...)
		return f.setLines(out)
	}
	// Insert after the last record of the same group (group=) or family
	// in the wanted section; when there is none, before the maintenance
	// windows and status pages (or at the end), opening the section if
	// the file ends in another one.
	group := ""
	for _, o := range r.Opts {
		if o.Key == "group" {
			group = o.Value
		}
	}
	at, inGroup, before, area, endArea := -1, -1, -1, "", ""
	var prev, groupPrev *Stmt
	anySection := !IsMonitorType(r.Type) // only monitors belong to sections
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if s.isDirective() {
			if d := s.directive(); d.Name == "SCOPE" {
				area = scopeSpec(d)
			}
			continue
		}
		rec, d := s.record()
		if d != nil {
			continue
		}
		if IsMonitorType(r.Type) && before < 0 && (rec.Type == "STATUSPAGE" || rec.Type == "MAINT") {
			before = s.StartLine - 1
		}
		if sameFamily(rec.Type, r.Type) && (anySection || area == r.Area) {
			at, prev = s.EndLine, s
			if group != "" && optValue(rec, "group") == group {
				inGroup, groupPrev = s.EndLine, s
			}
		}
	}
	if inGroup >= 0 {
		at, prev = inGroup, groupPrev // aligned like its group's records
	}
	endArea = area
	var add []string
	if at < 0 && before >= 0 && r.Area == "" {
		at = before
	}
	if at < 0 {
		at = len(ls)
		for at > 0 && strings.TrimSpace(ls[at-1]) == "" {
			at--
		}
		if !anySection && endArea != r.Area {
			spec := r.Area
			if spec == "" {
				spec = "*"
			}
			add = append(add, "$SCOPE "+spec+cr)
		}
	}
	line := r.Render()
	if prev != nil && prev.StartLine == prev.EndLine && add == nil {
		line = alignLike(ls[prev.StartLine-1], r)
	}
	out := append([]string{}, ls[:at]...)
	out = append(out, add...)
	out = append(out, line+cr)
	out = append(out, ls[at:]...)
	return f.setLines(out)
}

func optValue(r Record, key string) string {
	for _, o := range r.Opts {
		if o.Key == key {
			return o.Value
		}
	}
	return ""
}

// alignLike renders r with the columns of a neighbouring record line
// (name, interval, IN, type and target lined up), or plainly when the
// neighbour is not aligned or r does not fit its columns.
func alignLike(neighbour string, r RecordSpec) string {
	lx := &lexer{}
	toks, _ := lx.lineTokens(strings.TrimRight(neighbour, "\r"), 1)
	in := -1
	for i, t := range toks {
		if strings.EqualFold(t.Text, "IN") && i > 0 {
			in = i
			break
		}
	}
	if in < 0 || in+1 >= len(toks) {
		return r.Render()
	}
	col := func(i int) int { return toks[i].Pos.Col - 1 }
	var b strings.Builder
	pad := func(to int) {
		n := to - len([]rune(b.String()))
		if n < 1 {
			n = 1
		}
		b.WriteString(strings.Repeat(" ", n))
	}
	b.WriteString(r.Name)
	if r.Interval != "" {
		if in == 2 { // the neighbour has an interval column
			pad(col(1))
		} else {
			b.WriteString(" ")
		}
		b.WriteString(r.Interval)
	}
	pad(col(in))
	b.WriteString("IN")
	pad(col(in + 1))
	b.WriteString(strings.ToUpper(r.Type))
	if r.Target != "" {
		if in+2 < len(toks) {
			pad(col(in + 2))
		} else {
			b.WriteString(" ")
		}
		b.WriteString(quote(r.Target))
	}
	for _, o := range r.Opts {
		b.WriteString(" " + o.Key + "=" + quote(o.Value))
	}
	if r.Comment != "" {
		b.WriteString(" ; " + r.Comment)
	}
	return b.String()
}

// areaOf is the section a statement is written in.
func (f *File) areaOf(stmt *Stmt) string {
	area := ""
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if s == stmt {
			break
		}
		if s.isDirective() {
			if d := s.directive(); d.Name == "SCOPE" {
				area = scopeSpec(d)
			}
		}
	}
	return area
}

// scopeSpec is the section a $SCOPE directive opens ("" for global).
func scopeSpec(d Directive) string {
	if len(d.Args) == 0 || d.Args[0].Text == "*" {
		return ""
	}
	return d.Args[0].Text
}

func sameFamily(a, b string) bool {
	a, b = strings.ToUpper(a), strings.ToUpper(b)
	if IsMonitorType(a) && IsMonitorType(b) {
		return true
	}
	return a == b
}

// DeleteRecord removes a record and the comment lines directly attached
// above it (no blank line in between). Section comments (";;", "; ---")
// and comments separated by a blank line are kept.
func (f *File) DeleteRecord(name string) error {
	s, _ := f.findRecord(name)
	if s == nil {
		return fmt.Errorf("record %q not found", name)
	}
	ls := f.lines()
	start := s.StartLine - 1
	for start > 0 {
		prev := strings.TrimSpace(strings.TrimSuffix(ls[start-1], "\r"))
		if !strings.HasPrefix(prev, ";") || strings.HasPrefix(prev, ";;") || strings.HasPrefix(prev, "; ---") || strings.HasPrefix(prev, ";---") {
			break
		}
		start--
	}
	out := append([]string{}, ls[:start]...)
	out = append(out, ls[s.EndLine:]...)
	return f.setLines(out)
}

// RenameRecord renames a record in place.
func (f *File) RenameRecord(oldName, newName string) error {
	if !ValidName(newName) {
		return fmt.Errorf("invalid name %q", newName)
	}
	if s, _ := f.findRecord(newName); s != nil {
		return fmt.Errorf("name %q already in use", newName)
	}
	s, r := f.findRecord(oldName)
	if s == nil {
		return fmt.Errorf("record %q not found", oldName)
	}
	ls := f.lines()
	line := ls[r.NamePos.Line-1]
	idx := byteIndexOfCol(line, r.NamePos.Col)
	ls[r.NamePos.Line-1] = line[:idx] + newName + line[idx+len(oldName):]
	if err := f.setLines(ls); err != nil {
		return err
	}
	if f.Renamed == nil {
		f.Renamed = map[string]string{}
	}
	for o, n := range f.Renamed {
		if n == oldName { // a → b → c is a → c
			f.Renamed[o] = newName
			return nil
		}
	}
	f.Renamed[oldName] = newName
	return nil
}

// SetTunables rewrites the $SET directives: set adds or changes keys,
// unset removes them. Existing $SET lines are updated in place; new keys
// go on a new $SET line after the last directive (or the file header).
func (f *File) SetTunables(set map[string]string, unset []string) error {
	for k, v := range set {
		if !optKeyRe.MatchString(k) || oneLine(k, v) != nil {
			return fmt.Errorf("invalid tunable %q", k)
		}
	}
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	pending := map[string]string{}
	for k, v := range set {
		pending[k] = v
	}
	ls := f.lines()
	cr := f.eol()
	lastHeader := 0
	var remove []int
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if !s.isDirective() {
			if r, d := s.record(); d == nil && r.Type == "SOA" {
				lastHeader = s.EndLine
			}
			continue
		}
		d := s.directive()
		lastHeader = s.EndLine
		if d.Name != "SET" {
			continue
		}
		var opts []string
		for _, t := range d.Args {
			k, v, ok := t.option()
			if !ok {
				opts = append(opts, t.Raw)
				continue
			}
			if drop[k] {
				continue
			}
			if nv, ok := pending[k]; ok {
				v = nv
				delete(pending, k)
			}
			opts = append(opts, k+"="+quote(v))
		}
		if len(opts) == 0 {
			remove = append(remove, s.StartLine)
			continue
		}
		line := "$SET " + strings.Join(opts, " ")
		if s.Comment != "" {
			line += " ; " + s.Comment
		}
		ls[s.StartLine-1] = line + cr
		for l := s.StartLine; l < s.EndLine; l++ {
			ls[l] = "\x00"
		}
	}
	for _, l := range remove {
		ls[l-1] = "\x00"
	}
	if len(pending) > 0 {
		keys := make([]string, 0, len(pending))
		for k := range pending {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var opts []string
		for _, k := range keys {
			opts = append(opts, k+"="+quote(pending[k]))
		}
		newLine := "$SET " + strings.Join(opts, " ") + cr
		ls = append(ls[:lastHeader], append([]string{newLine}, ls[lastHeader:]...)...)
	}
	out := ls[:0]
	for _, l := range ls {
		if l != "\x00" {
			out = append(out, l)
		}
	}
	return f.setLines(out)
}

// MaxSerial is the largest serial.
const MaxSerial = 4294967295

// NextSerial computes the serial for a change made by unagikuma itself:
// max(cur+1, YYYYMMDD(UTC)*100+1). When that would move the date part more
// than 7 days ahead of cur's, it uses cur+1 and returns a warning (a wrong
// clock must not burn serials).
func NextSerial(cur uint32, now time.Time) (uint32, string, error) {
	if cur >= MaxSerial {
		return 0, "", errors.New("serial is at its maximum; reset it with `unagikuma serial reset`")
	}
	d := now.UTC()
	today := uint64(d.Year()*10000+int(d.Month())*100+d.Day()) * 100
	cand := uint64(cur) + 1
	if today+1 > cand && today+1 <= MaxSerial {
		curDate, ok := serialDate(cur)
		if ok && today/100 > uint64(curDate.Year()*10000+int(curDate.Month())*100+curDate.Day()) {
			if d.Sub(curDate) > 7*24*time.Hour {
				return uint32(cand), fmt.Sprintf("today's date is more than 7 days after the serial's date; used %d instead (check the clock)", cand), nil
			}
		}
		cand = today + 1
	}
	if cand > MaxSerial {
		return 0, "", errors.New("serial would overflow")
	}
	return uint32(cand), "", nil
}

// serialDate interprets a YYYYMMDDnn serial.
func serialDate(s uint32) (time.Time, bool) {
	if s < 1970010100 {
		return time.Time{}, false
	}
	day := int(s / 100)
	t := time.Date(day/10000, time.Month(day/100%100), day%100, 0, 0, 0, 0, time.UTC)
	if t.Year() != day/10000 || int(t.Month()) != day/100%100 || t.Day() != day%100 {
		return time.Time{}, false
	}
	return t, true
}

// CheckSerial validates a new serial against the active one.
func CheckSerial(newSerial, active uint32) error {
	switch {
	case newSerial == 0:
		return errors.New("the revision must be at least 1")
	case active != 0 && newSerial < active:
		return fmt.Errorf("revision %d is lower than the active %d (use history → rollback to go back)", newSerial, active)
	case active != 0 && uint64(newSerial) > uint64(active)+1_000_000:
		return fmt.Errorf("revision %d jumps more than 1000000 above the active %d (typo?)", newSerial, active)
	}
	return nil
}

// RecordETags returns an entity tag for every record, for optimistic
// concurrency on single records: it changes when the record's meaning
// changes (name, interval, type, target, options), not when the file is
// re-spaced, commented or reordered, or when another record changes.
func (f *File) RecordETags() map[string]string {
	out := map[string]string{}
	for i := range f.Stmts {
		if r, ok := StmtRecord(&f.Stmts[i]); ok && r.Type != "SOA" {
			out[r.Name] = recordETag(r)
		}
	}
	return out
}

// RecordETag is the entity tag of one record ("" when there is none).
func (f *File) RecordETag(name string) string {
	r, ok := f.Record(name)
	if !ok {
		return ""
	}
	return recordETag(r)
}

func recordETag(r Record) string {
	opts := make([]string, 0, len(r.Opts))
	for _, o := range r.Opts {
		opts = append(opts, o.Key+"="+o.Value)
	}
	sort.Strings(opts)
	h := sha256.New()
	for _, p := range append([]string{r.Name, r.Interval, r.Type, r.Target}, opts...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return `"r` + hex.EncodeToString(h.Sum(nil)[:9]) + `"`
}

// EndArea is the section ($SCOPE) the file ends in ("" = global): text
// appended to the file belongs to it.
func (f *File) EndArea() string {
	area := ""
	for i := range f.Stmts {
		s := &f.Stmts[i]
		if s.isDirective() {
			if d := s.directive(); d.Name == "SCOPE" {
				area = scopeSpec(d)
			}
		}
	}
	return area
}

// AppendBlock adds a block of lines (comments, records, $SCOPE) at the end
// of the file, after one blank line, in the file's line endings. The
// result must parse; the file is unchanged otherwise.
func (f *File) AppendBlock(block string) error {
	ls := f.lines()
	for len(ls) > 0 && strings.TrimSpace(strings.TrimSuffix(ls[len(ls)-1], "\r")) == "" {
		ls = ls[:len(ls)-1]
	}
	cr := f.eol()
	out := append([]string{}, ls...)
	if len(out) > 0 {
		out = append(out, cr)
	}
	for _, l := range strings.Split(strings.TrimRight(strings.ReplaceAll(block, "\r\n", "\n"), "\n"), "\n") {
		out = append(out, l+cr)
	}
	out = append(out, "")
	return f.setLines(out)
}

// SetOpts changes options of a record, keeping the others in place: a key
// set to "" is removed, a new key is added at the end.
func (f *File) SetOpts(name string, set map[string]string) error {
	r, ok := f.Record(name)
	if !ok {
		return fmt.Errorf("no record %s", name)
	}
	done := map[string]bool{}
	var opts []Opt
	for _, o := range r.Opts {
		v, change := set[o.Key]
		switch {
		case !change:
			opts = append(opts, o)
		case v != "" && !done[o.Key]:
			opts = append(opts, Opt{Key: o.Key, Value: v})
		}
		done[o.Key] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !done[k] && set[k] != "" {
			opts = append(opts, Opt{Key: k, Value: set[k]})
		}
	}
	return f.UpsertRecord(RecordSpec{Name: r.Name, Type: r.Type, Target: r.Target, Interval: r.Interval, Opts: opts})
}
