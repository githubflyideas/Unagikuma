package postmortem

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// Formats a report can be written in.
var Formats = []string{"md", "json", "html"}

// Write renders the report as md, json or html.
func (r *Report) Write(w io.Writer, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "html":
		return r.writeHTML(w)
	case "", "md", "markdown":
		_, err := io.WriteString(w, r.Markdown())
		return err
	}
	return fmt.Errorf("unknown format %q (md, json or html)", format)
}

func (r *Report) t(key string, args ...any) string { return i18n.T(r.Lang, "pm."+key, args...) }

func (r *Report) name() string {
	if r.Incident.Monitor != "" {
		return r.Incident.Monitor
	}
	return r.Incident.Title
}

// facts is the summary table, label and value.
func (r *Report) facts() [][2]string {
	out := [][2]string{
		{r.t("f.failed"), r.at(r.FailedAt)},
		{r.t("f.detected"), r.at(r.DetectedAt)},
		{r.t("f.notified"), r.at(r.NotifiedAt)},
	}
	if !r.NotifiedAt.IsZero() {
		out[2][1] += " (+" + r.dur(r.NotifiedAt.Sub(r.DetectedAt)) + ")"
	}
	if !r.AckedAt.IsZero() {
		out = append(out, [2]string{r.t("f.acked"), r.at(r.AckedAt)})
	}
	out = append(out, [2]string{r.t("f.resolved"), r.at(r.ResolvedAt)})
	if len(r.Recipients) > 0 {
		out = append(out, [2]string{r.t("f.recipients"), strings.Join(r.Recipients, ", ")})
	}
	if r.Class != "" {
		out = append(out, [2]string{r.t("f.class"), r.t("class." + r.Class)})
	}
	return out
}

func (r *Report) livenessLines() []string {
	l := r.Liveness
	if !l.Checked {
		return []string{r.t("live.unknown")}
	}
	lines := []string{r.t("live.rounds", "n", l.Rounds, "from", r.at(l.From), "to", r.at(l.To))}
	if len(l.Gaps) == 0 {
		lines = append(lines, r.t("live.no_gaps"))
	}
	for _, g := range l.Gaps {
		lines = append(lines, r.t("live.gap", "from", r.at(g.From), "to", r.at(g.To), "for", r.dur(g.To.Sub(g.From))))
	}
	for _, e := range l.Events {
		lines = append(lines, r.at(e.At)+" — "+e.What)
	}
	return lines
}

func (r *Report) coverageLines() []string {
	c := r.Coverage
	if !c.Known {
		return []string{r.t("cov.unknown")}
	}
	lines := []string{r.t("cov.what", "type", c.Type, "target", c.Target)}
	if c.Depth != "" {
		lines = append(lines, r.t("cov.depth."+c.Depth))
	}
	if c.Interval > 0 {
		lines = append(lines, r.t("cov.every", "interval", r.dur(c.Interval), "attempts", max(1, c.Attempts), "timeout", r.dur(c.Timeout)))
	}
	if len(c.Nodes) > 0 {
		q := c.Quorum
		if q == "" {
			q = "majority"
		}
		lines = append(lines, r.t("cov.nodes", "nodes", strings.Join(c.Nodes, ", "), "quorum", q))
	} else {
		lines = append(lines, r.t("cov.local"))
	}
	lines = append(lines, r.t("cov.limit"))
	return lines
}

// Markdown renders the report as Markdown.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", mdEsc(r.t("title", "id", r.Incident.ID, "name", r.name())))
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", r.t("conclusion"), mdEsc(r.Conclusion))
	fmt.Fprintf(&b, "## %s\n\n| | |\n|---|---|\n", r.t("summary"))
	for _, f := range r.facts() {
		fmt.Fprintf(&b, "| %s | %s |\n", mdCell(f[0]), mdCell(f[1]))
	}
	b.WriteString("\n")
	for i, s := range r.Segments {
		fmt.Fprintf(&b, "## %d. %s\n\n", i+1, s.Title)
		for _, e := range s.Entries {
			mark := ""
			if e.Mark == "warn" {
				mark = "⚠ "
			}
			at := ""
			if !e.At.IsZero() {
				at = "`" + r.at(e.At) + "` "
			}
			fmt.Fprintf(&b, "- %s%s%s\n", at, mark, mdEsc(e.What))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## %s\n\n", r.t("liveness"))
	for _, l := range r.livenessLines() {
		fmt.Fprintf(&b, "- %s\n", mdEsc(l))
	}
	fmt.Fprintf(&b, "\n## %s\n\n", r.t("coverage"))
	for _, l := range r.coverageLines() {
		fmt.Fprintf(&b, "- %s\n", mdEsc(l))
	}
	fmt.Fprintf(&b, "\n_%s_\n", mdEsc(r.t("generated", "at", r.at(r.Generated))))
	return b.String()
}

func mdEsc(s string) string {
	return strings.NewReplacer("\n", " ", "<", "&lt;", ">", "&gt;").Replace(s)
}

func mdCell(s string) string { return strings.ReplaceAll(mdEsc(s), "|", "\\|") }

func (r *Report) writeHTML(w io.Writer) error {
	e := html.EscapeString
	var b strings.Builder
	dir := "ltr"
	if r.Lang == "ar" || r.Lang == "ur" {
		dir = "rtl"
	}
	title := r.t("title", "id", r.Incident.ID, "name", r.name())
	fmt.Fprintf(&b, `<!doctype html><html lang="%s" dir="%s"><head><meta charset="utf-8"><title>%s</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem;color:#222}
table{border-collapse:collapse}td{padding:.2rem .8rem;border-bottom:1px solid #ddd}td:first-child{color:#666}
li.warn{color:#9a5b00}time{font-family:ui-monospace,monospace;color:#555;margin-inline-end:.5rem}
.concl{background:#f4f6f8;padding:.8rem 1rem;border-radius:6px}</style></head><body>
`, e(r.Lang), dir, e(title))
	fmt.Fprintf(&b, "<h1>%s</h1>\n<h2>%s</h2><p class=\"concl\">%s</p>\n<h2>%s</h2><table>\n", e(title), e(r.t("conclusion")), e(r.Conclusion), e(r.t("summary")))
	for _, f := range r.facts() {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td></tr>\n", e(f[0]), e(f[1]))
	}
	b.WriteString("</table>\n")
	for i, s := range r.Segments {
		fmt.Fprintf(&b, "<h2>%d. %s</h2><ul>\n", i+1, e(s.Title))
		for _, en := range s.Entries {
			at := ""
			if !en.At.IsZero() {
				at = fmt.Sprintf(`<time datetime="%s">%s</time>`, en.At.UTC().Format(time.RFC3339), e(r.at(en.At)))
			}
			fmt.Fprintf(&b, "<li class=\"%s\">%s%s</li>\n", e(en.Mark), at, e(en.What))
		}
		b.WriteString("</ul>\n")
	}
	for _, sec := range []struct {
		h     string
		lines []string
	}{{r.t("liveness"), r.livenessLines()}, {r.t("coverage"), r.coverageLines()}} {
		fmt.Fprintf(&b, "<h2>%s</h2><ul>\n", e(sec.h))
		for _, l := range sec.lines {
			fmt.Fprintf(&b, "<li>%s</li>\n", e(l))
		}
		b.WriteString("</ul>\n")
	}
	fmt.Fprintf(&b, "<p><small>%s</small></p></body></html>\n", e(r.t("generated", "at", r.at(r.Generated))))
	_, err := io.WriteString(w, b.String())
	return err
}
