package config

import (
	"fmt"
	"sort"
	"strings"
)

// Change is one record-level difference between two configurations.
type Change struct {
	Kind   string      `json:"kind"`   // added | removed | changed
	Record string      `json:"record"` // record name
	Type   string      `json:"type"`   // MONITOR CHANNEL POLICY NODE STATUSPAGE SETTINGS
	Class  ChangeClass `json:"class"`  // for changed monitors
	Fields []string    `json:"fields,omitempty"`
}

// Summary counts changes.
type Summary struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

// Summarize counts a change list.
func Summarize(cs []Change) Summary {
	var s Summary
	for _, c := range cs {
		switch c.Kind {
		case "added":
			s.Added++
		case "removed":
			s.Removed++
		default:
			s.Changed++
		}
	}
	return s
}

// Diff compares two configurations record by record (effective values).
func Diff(a, b *Config) []Change {
	var out []Change
	am, bm := map[string]*Monitor{}, map[string]*Monitor{}
	for _, m := range a.Monitors {
		am[m.Name] = m
	}
	for _, m := range b.Monitors {
		bm[m.Name] = m
	}
	for _, m := range b.Monitors {
		old, ok := am[m.Name]
		if !ok {
			out = append(out, Change{Kind: "added", Record: m.Name, Type: "MONITOR"})
			continue
		}
		if cls := ClassifyChange(old, m); cls != ChangeNone {
			out = append(out, Change{Kind: "changed", Record: m.Name, Type: "MONITOR", Class: cls, Fields: diffOpts(old, m)})
		}
	}
	for _, m := range a.Monitors {
		if _, ok := bm[m.Name]; !ok {
			out = append(out, Change{Kind: "removed", Record: m.Name, Type: "MONITOR"})
		}
	}
	out = append(out, diffNamed("CHANNEL", names(a.Channels, func(c *Channel) (string, string) { return c.Name, fmt.Sprint(c.Kind, c.Opts, c.Retries, c.Rate) }),
		names(b.Channels, func(c *Channel) (string, string) { return c.Name, fmt.Sprint(c.Kind, c.Opts, c.Retries, c.Rate) }))...)
	out = append(out, diffNamed("POLICY", names(a.Policies, func(p *Policy) (string, string) { return p.Name, fmt.Sprintf("%+v", *p) }),
		names(b.Policies, func(p *Policy) (string, string) { return p.Name, fmt.Sprintf("%+v", *p) }))...)
	out = append(out, diffNamed("NODE", names(a.Nodes, func(n *Node) (string, string) { return n.Name, fmt.Sprint(n.Opts, n.Description) }),
		names(b.Nodes, func(n *Node) (string, string) { return n.Name, fmt.Sprint(n.Opts, n.Description) }))...)
	out = append(out, diffNamed("STATUSPAGE", names(a.StatusPages, func(s *StatusPage) (string, string) { return s.Name, fmt.Sprintf("%+v", *s) }),
		names(b.StatusPages, func(s *StatusPage) (string, string) { return s.Name, fmt.Sprintf("%+v", *s) }))...)
	if fmt.Sprint(a.Defaults, a.Quorum, a.Dialect, a.Tunables.Overrides(), a.Tags) != fmt.Sprint(b.Defaults, b.Quorum, b.Dialect, b.Tunables.Overrides(), b.Tags) {
		out = append(out, Change{Kind: "changed", Record: "@", Type: "SETTINGS"})
	}
	return out
}

func names[T any](xs []T, f func(T) (string, string)) map[string]string {
	m := map[string]string{}
	for _, x := range xs {
		k, v := f(x)
		m[k] = v
	}
	return m
}

func diffNamed(typ string, a, b map[string]string) []Change {
	var out []Change
	keys := make([]string, 0, len(a)+len(b))
	seen := map[string]bool{}
	for k := range a {
		keys, seen[k] = append(keys, k), true
	}
	for k := range b {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		av, aok := a[k]
		bv, bok := b[k]
		switch {
		case !aok:
			out = append(out, Change{Kind: "added", Record: k, Type: typ})
		case !bok:
			out = append(out, Change{Kind: "removed", Record: k, Type: typ})
		case av != bv:
			out = append(out, Change{Kind: "changed", Record: k, Type: typ})
		}
	}
	return out
}

func diffOpts(a, b *Monitor) []string {
	var out []string
	if a.Type != b.Type {
		out = append(out, "type")
	}
	if a.Target != b.Target {
		out = append(out, "target")
	}
	if a.Interval != b.Interval {
		out = append(out, "interval")
	}
	keys := map[string]bool{}
	for k := range a.Opts {
		keys[k] = true
	}
	for k := range b.Opts {
		keys[k] = true
	}
	for k := range keys {
		if a.Opts[k] != b.Opts[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// UnifiedDiff renders a line diff of two texts (for history views).
func UnifiedDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	// LCS table; files are small (hundreds to a few thousand lines).
	n, m := len(al), len(bl)
	if n*m > 25_000_000 {
		return "(files too large to diff)"
	}
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			out.WriteString("  " + al[i] + "\n")
			i++
			j++
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+ " + bl[j] + "\n")
			j++
		default:
			out.WriteString("- " + al[i] + "\n")
			i++
		}
	}
	return out.String()
}
