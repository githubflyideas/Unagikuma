// Package appprofile describes the status page of a well-known application
// as data: where it is, how to read it, and what each number on it is (its
// type and unit). A monitor of type APP names a profile; the HTTP check
// fetches the page and the profile turns it into numbers the value rules
// judge, so adding an application is adding a file under profiles/, not
// writing a check.
//
// Two readers cover the status pages that exist: "text" (lines such as
// "Busy Workers: 3", or a pattern with one capture group) and "metrics"
// (the Prometheus text format that many programs serve at /metrics).
// A page that does not look like the application's own is an error that
// says so, never a silent zero.
package appprofile

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed profiles/*.json
var files embed.FS

// Field is one number a profile reads.
type Field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Type is "quantity" (a number to compare with limits) or "counter"
	// (a total that only grows: it is judged by its rate per second).
	Type string `json:"type"`
	// Unit is "" (a plain count or ratio), "%", "bps", "°C", "B" (bytes)
	// or "s" (seconds): what limits written with a unit may
	// measure.
	Unit     string `json:"unit,omitempty"`
	Optional bool   `json:"optional,omitempty"` // an older version may not have it

	// text reader: Line is the name before a colon ("Busy Workers" in
	// "Busy Workers: 3"); Regex is a pattern with exactly one capture
	// group holding the number.
	Line  string `json:"line,omitempty"`
	Regex string `json:"regex,omitempty"`

	// metrics reader: the metric's name, the labels a series must carry,
	// and how the matching series are combined: sum (default), max, min or
	// count.
	Metric string            `json:"metric,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Agg    string            `json:"agg,omitempty"`

	re *regexp.Regexp
}

// Profile is one application.
type Profile struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reader string `json:"reader"` // text | metrics
	// Transport is how the page is fetched: "http" (the default; an APP
	// monitor) or "redis" (the reply to INFO, read by a REDIS monitor's
	// field=).
	Transport string `json:"transport,omitempty"`
	// Path is where the page is when the monitor's address has none; Port
	// is the port when it has none either.
	Path string `json:"path"`
	Port int    `json:"port"`
	// Docs is the application's own documentation of the page; Needs says
	// what must be switched on in the application for the page to exist.
	Docs  string `json:"docs"`
	Needs string `json:"needs"`
	// Verified says what the profile has been checked against: "docs" (the
	// documented format, with sample replies written from it) or the
	// version of a live instance it was run on.
	Verified string `json:"verified"`
	// Primary is the field judged when the monitor names none; Summary the
	// fields shown in the result's text.
	Primary string   `json:"primary"`
	Summary []string `json:"summary"`
	Fields  []Field  `json:"fields"`
}

// Field returns a field by key.
func (p *Profile) Field(key string) (*Field, bool) {
	for i := range p.Fields {
		if p.Fields[i].Key == key {
			return &p.Fields[i], true
		}
	}
	return nil, false
}

var registry = map[string]*Profile{}

func init() {
	entries, err := files.ReadDir("profiles")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		b, err := files.ReadFile("profiles/" + e.Name())
		if err != nil {
			panic(err)
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			panic(fmt.Sprintf("profile %s: %v", e.Name(), err))
		}
		if p.Transport == "" {
			p.Transport = "http"
		}
		if err := p.prepare(); err != nil {
			panic(fmt.Sprintf("profile %s: %v", e.Name(), err))
		}
		registry[p.ID] = &p
	}
}

// prepare checks a profile and compiles its patterns, so a broken file
// stops the program at once (the tests load them all) and never at the
// first monitor that uses it.
func (p *Profile) prepare() error {
	if p.ID == "" || p.Title == "" {
		return fmt.Errorf("needs an id and a title")
	}
	if p.Reader != "text" && p.Reader != "metrics" {
		return fmt.Errorf("reader must be text or metrics")
	}
	switch p.Transport {
	case "http":
		if p.Path == "" || !strings.HasPrefix(p.Path, "/") {
			return fmt.Errorf("needs a path starting with /")
		}
	case "redis":
		if p.Reader != "text" {
			return fmt.Errorf("the redis transport is read as text")
		}
	default:
		return fmt.Errorf("transport must be http or redis")
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("needs a port")
	}
	if len(p.Fields) == 0 {
		return fmt.Errorf("has no fields")
	}
	seen := map[string]bool{}
	for i := range p.Fields {
		f := &p.Fields[i]
		if f.Key == "" || f.Label == "" || seen[f.Key] {
			return fmt.Errorf("field %q: needs a unique key and a label", f.Key)
		}
		seen[f.Key] = true
		if f.Type != "quantity" && f.Type != "counter" {
			return fmt.Errorf("field %s: type must be quantity or counter", f.Key)
		}
		switch f.Unit {
		case "", "%", "bps", "°C", "B", "s":
		default:
			return fmt.Errorf("field %s: unit must be empty, %%, bps, °C, B or s", f.Key)
		}
		switch p.Reader {
		case "text":
			pat := f.Regex
			if (pat == "") == (f.Line == "") {
				return fmt.Errorf("field %s: give a line or a regex", f.Key)
			}
			if pat == "" {
				pat = `^\s*` + regexp.QuoteMeta(f.Line) + `:\s*(` + number + `)\s*$`
			}
			re, err := regexp.Compile(`(?m)` + pat)
			if err != nil {
				return fmt.Errorf("field %s: %v", f.Key, err)
			}
			if re.NumSubexp() != 1 {
				return fmt.Errorf("field %s: the pattern needs exactly one capture group", f.Key)
			}
			f.re = re
		case "metrics":
			if f.Metric == "" {
				return fmt.Errorf("field %s: needs a metric", f.Key)
			}
			switch f.Agg {
			case "", "sum", "max", "min", "count":
			default:
				return fmt.Errorf("field %s: agg must be sum, max, min or count", f.Key)
			}
		}
	}
	if _, ok := p.Field(p.Primary); !ok {
		return fmt.Errorf("primary %q is not a field", p.Primary)
	}
	for _, k := range p.Summary {
		if _, ok := p.Field(k); !ok {
			return fmt.Errorf("summary %q is not a field", k)
		}
	}
	return nil
}

// number is a decimal such as 12, .5, -3.25 or 1e3.
const number = `[-+]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][-+]?[0-9]+)?`

// Get returns the profile with that id.
func Get(id string) (*Profile, bool) {
	p, ok := registry[id]
	return p, ok
}

// App returns an HTTP application profile (what app= of an APP monitor
// names).
func App(id string) (*Profile, bool) {
	p, ok := registry[id]
	return p, ok && p.Transport == "http"
}

// All lists the profiles by id.
func All() []*Profile {
	out := make([]*Profile, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IDs lists the ids of the HTTP application profiles, for messages.
func IDs() []string {
	var ids []string
	for _, p := range All() {
		if p.Transport == "http" {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// Select keeps only the series that carry a label with a value (the
// metrics reader); the zero value keeps all.
type Select struct{ Label, Value string }

// ParseSelect reads "label=value".
func ParseSelect(s string) (Select, error) {
	l, v, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(l) == "" || strings.TrimSpace(v) == "" {
		return Select{}, fmt.Errorf("select must be label=value, got %q", s)
	}
	return Select{strings.TrimSpace(l), strings.TrimSpace(v)}, nil
}

// Read turns a reply into the profile's numbers, by field key. A reply
// that lacks a field the profile needs is not the application's page: the
// error says what is missing.
func (p *Profile) Read(body []byte, sel Select) (map[string]float64, error) {
	vals := map[string]float64{}
	var missing []string
	switch p.Reader {
	case "text":
		for i := range p.Fields {
			f := &p.Fields[i]
			m := f.re.FindSubmatch(body)
			if m == nil {
				if !f.Optional {
					missing = append(missing, f.Label)
				}
				continue
			}
			v, err := strconv.ParseFloat(string(m[1]), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				missing = append(missing, f.Label)
				continue
			}
			vals[f.Key] = v
		}
	case "metrics":
		series := parseMetrics(body)
		for i := range p.Fields {
			f := &p.Fields[i]
			v, ok := f.aggregate(series, sel)
			if !ok {
				if !f.Optional {
					missing = append(missing, f.Label)
				}
				continue
			}
			vals[f.Key] = v
		}
	}
	if sel.Label != "" {
		// Narrowed to one thing (a proxy): the fields that belong to other
		// things have no series for it, which is not a wrong page. Nothing
		// at all is.
		if len(vals) == 0 {
			return nil, fmt.Errorf("nothing on the %s status page has %s=%s", p.Title, sel.Label, sel.Value)
		}
		return vals, nil
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("this is not the %s status page: %s not found", p.Title, strings.Join(missing, ", "))
	}
	return vals, nil
}

// Describe is the result's text: the summary fields and their values.
func (p *Profile) Describe(vals map[string]float64) string {
	var parts []string
	for _, k := range p.Summary {
		f, _ := p.Field(k)
		if v, ok := vals[k]; ok {
			parts = append(parts, f.Label+" "+strconv.FormatFloat(v, 'f', -1, 64))
		}
	}
	return p.Title + ": " + strings.Join(parts, ", ")
}

// ---- the Prometheus text format ----

type series struct {
	name   string
	labels map[string]string
	value  float64
}

// parseMetrics reads the text exposition format: "name{a="b"} 1.5 [time]"
// lines, with # comments. Lines it cannot read are skipped.
func parseMetrics(body []byte) []series {
	var out []series
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.IndexAny(line, "{ \t")
		if i <= 0 {
			continue
		}
		s := series{name: line[:i], labels: map[string]string{}}
		rest := line[i:]
		if rest[0] == '{' {
			end, labels, ok := parseLabels(rest)
			if !ok {
				continue
			}
			s.labels, rest = labels, rest[end:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		s.value = v
		out = append(out, s)
	}
	return out
}

// parseLabels reads {a="b",c="d"} from the start of s and returns the
// index after the closing brace.
func parseLabels(s string) (int, map[string]string, bool) {
	labels := map[string]string{}
	i := 1
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i < len(s) && s[i] == '}' {
			return i + 1, labels, true
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 || i+eq+1 >= len(s) || s[i+eq+1] != '"' {
			return 0, nil, false
		}
		name := strings.TrimSpace(s[i : i+eq])
		j := i + eq + 2
		var val strings.Builder
		for j < len(s) && s[j] != '"' {
			if s[j] == '\\' && j+1 < len(s) {
				j++
				switch s[j] {
				case 'n':
					val.WriteByte('\n')
				default:
					val.WriteByte(s[j])
				}
			} else {
				val.WriteByte(s[j])
			}
			j++
		}
		if j >= len(s) {
			return 0, nil, false
		}
		labels[name] = val.String()
		i = j + 1
	}
	return 0, nil, false
}

// aggregate combines the series a field names.
func (f *Field) aggregate(all []series, sel Select) (float64, bool) {
	var vals []float64
next:
	for _, s := range all {
		if s.name != f.Metric {
			continue
		}
		for k, want := range f.Labels {
			if s.labels[k] != want {
				continue next
			}
		}
		if sel.Label != "" && s.labels[sel.Label] != sel.Value {
			continue
		}
		vals = append(vals, s.value)
	}
	if len(vals) == 0 {
		return 0, false
	}
	switch f.Agg {
	case "count":
		return float64(len(vals)), true
	case "max", "min":
		r := vals[0]
		for _, v := range vals[1:] {
			if (f.Agg == "max" && v > r) || (f.Agg == "min" && v < r) {
				r = v
			}
		}
		return r, true
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	return sum, true
}

// LimitUnit is the unit the value rules judge the field in. A field
// without a unit is a plain number, which is known and not the same as a
// value nobody has described: 80% is not a limit on a count.
func (f *Field) LimitUnit() string {
	if f.Unit == "" {
		return "1"
	}
	return f.Unit
}

// A field enters a profile only when its meaning is certain: it has been
// read on a running instance of the application, its unit is known, and
// every value it can take (a -1 for "never", a total that is absent until
// the first event) is something a limit can judge. A field nobody has
// seen, or whose value can say the opposite of what it is named after, is
// left out; a field an application lists only after its first event is
// marked optional, and it is shown only after that event has been seen.
