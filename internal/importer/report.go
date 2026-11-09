package importer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// What happened to one thing found in the other tool.
const (
	StatusImported  = "imported"  // carried over as it was
	StatusAttention = "attention" // carried over, but someone should look at it
	StatusSkipped   = "skipped"   // already here: left as it is
	StatusLeftOut   = "left-out"  // not imported
	StatusMerged    = "merged"    // the same check twice in the input: imported once
)

// Item is one line of the migration report.
type Item struct {
	Kind   string `json:"kind"`             // monitor, channel, policy, maintenance, status page, account, ...
	Source string `json:"source"`           // its name in the other tool
	Name   string `json:"name,omitempty"`   // its record name here
	Type   string `json:"type,omitempty"`   // what it is (HTTP, Telegram, MQTT...)
	Target string `json:"target,omitempty"` // what it checks or where it sends
	Group  string `json:"group,omitempty"`
	Status string `json:"status"`
	Why    string `json:"why,omitempty"`  // what changed or why it was left out
	Todo   string `json:"todo,omitempty"` // what to do about it
}

// section is a titled run of records in the imported block.
type section struct {
	title  string
	recs   []config.RecordSpec
	shared bool // channels, policies, periods, windows, pages: any $SCOPE
}

func (r *Result) add(it Item) *Item {
	r.Items = append(r.Items, it)
	return &r.Items[len(r.Items)-1]
}

// attention marks an item as needing a look, adding a reason and a fix.
func (it *Item) attention(why, todo string) {
	if it.Status == StatusImported {
		it.Status = StatusAttention
	}
	it.Why = joinSentences(it.Why, why)
	it.Todo = joinSentences(it.Todo, todo)
}

func joinSentences(a, b string) string {
	switch {
	case b == "":
		return a
	case a == "":
		return b
	}
	return a + " " + b
}

// count returns the number of items of a kind with one of the statuses.
func (r *Result) count(kind string, statuses ...string) int {
	n := 0
	for _, it := range r.Items {
		if (kind == "" || it.Kind == kind) && (len(statuses) == 0 || contains(statuses, it.Status)) {
			n++
		}
	}
	return n
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// finish fills in what the CLI, the API and the web UI read: the block of
// records, the counts and the notes.
func (r *Result) finish() {
	r.Blocked = r.ActivationError() != nil
	r.Records = nil
	for i := range r.sections {
		for j := range r.sections[i].recs {
			sortOpts(r.sections[i].recs[j].Opts)
		}
		r.Records = append(r.Records, r.sections[i].recs...)
	}
	r.Text = r.Render("")
	r.Notes = nil
	for _, it := range r.Items {
		if it.Status == StatusImported {
			continue
		}
		s := it.Source
		if it.Kind != "monitor" && it.Kind != "" {
			s = it.Kind + " " + s
		}
		switch it.Status {
		case StatusSkipped, StatusMerged:
			s += ": " + it.Why
		case StatusAttention:
			s += ": " + joinSentences(it.Why, it.Todo)
		default:
			s += ": not imported. " + joinSentences(it.Why, it.Todo)
		}
		r.Notes = append(r.Notes, s)
	}
}

// Render returns the records to append to monitors.conf, in sections with
// a header saying where they came from, aligned like `unagikuma fmt`.
// endArea is the $SCOPE the file ends in: the monitors open their own
// section when it differs.
func (r *Result) Render(endArea string) string {
	var b strings.Builder
	rule := "; " + strings.Repeat("=", 68)
	b.WriteString(rule + "\n")
	b.WriteString("; Imported from " + r.Source)
	if r.File != "" {
		b.WriteString(" (" + r.File + ")")
	}
	b.WriteString(" on " + r.When.Format("2006-01-02 15:04") + "\n")
	var sb strings.Builder
	writeWrapped(&sb, r.summaryLine(), 70, "; ", "; ")
	b.WriteString(sb.String())
	b.WriteString("; Names were made from the original names; title= keeps the original.\n")
	b.WriteString(rule + "\n")
	area := endArea
	for _, s := range r.sections {
		if len(s.recs) == 0 {
			continue
		}
		b.WriteString("\n")
		if !s.shared && area != r.Area {
			area = r.Area
			spec := area
			if spec == "" {
				spec = "*"
			}
			b.WriteString("$SCOPE " + spec + "\n\n")
		}
		b.WriteString("; --- " + s.title + "\n")
		for _, rec := range s.recs {
			b.WriteString(rec.Render() + "\n")
		}
	}
	return string(config.Format([]byte(b.String())))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// summaryLine says what was imported, e.g. "30 monitors, 9 channels".
func (r *Result) summaryLine() string {
	var parts []string
	for _, k := range []struct{ kind, one, many string }{
		{"monitor", "monitor", "monitors"}, {"channel", "notification channel", "notification channels"},
		{"contact group", "contact group", "contact groups"}, {"policy", "notification policy", "notification policies"},
		{"time period", "time period", "time periods"}, {"maintenance", "maintenance window", "maintenance windows"},
		{"status page", "status page", "status pages"}, {"account", "account", "accounts"},
	} {
		if n := r.count(k.kind, StatusImported, StatusAttention); n > 0 {
			parts = append(parts, plural(n, k.one, k.many))
		}
	}
	if len(parts) == 0 {
		return "nothing new"
	}
	return strings.Join(parts, ", ")
}

// ---- the report ----

// ReportOptions shape the report.
type ReportOptions struct {
	Width   int    // terminal columns (default 80)
	DryRun  bool   // a preview: nothing was changed
	Config  string // where the records went (or would go)
	Extra   []string
	Applied bool
}

// Report is the migration report for a terminal: a short summary, the
// imported monitors as a table, then what needs a look and what was left
// out, each with what to do.
func (r *Result) Report(o ReportOptions) string {
	w := o.Width
	if w <= 0 {
		w = 80
	}
	w = max(w, 40)
	var b strings.Builder
	title := r.Source + " → Unagikuma"
	if o.DryRun {
		title += " (preview: nothing was changed)"
	}
	writeWrapped(&b, title, w, "", "")
	b.WriteString(strings.Repeat("=", min(w, utf8.RuneCountInString(title))) + "\n\n")
	for _, l := range r.summaryRows() {
		writeWrapped(&b, fmt.Sprintf("%-14s %s", l[0], l[1]), w, "  ", strings.Repeat(" ", 17))
	}
	if o.Config != "" {
		b.WriteString("\n")
		verb := "Added to "
		if o.DryRun {
			verb = "Would be added to "
		}
		writeWrapped(&b, verb+o.Config, w, "", "  ")
	}
	if rows := r.monitorRows(); len(rows) > 0 {
		b.WriteString("\nMonitors\n--------\n")
		b.WriteString(table([]string{"Name", "Type", "Checks", "Group"}, rows, w))
	}
	if rows := r.otherRows(); len(rows) > 0 {
		b.WriteString("\nAlso imported\n-------------\n")
		b.WriteString(table([]string{"Name", "What", "Details"}, rows, w))
	}
	list := func(title string, status string) {
		var its []Item
		for _, it := range r.Items {
			if it.Status == status {
				its = append(its, it)
			}
		}
		if len(its) == 0 {
			return
		}
		b.WriteString("\n" + title + "\n" + strings.Repeat("-", utf8.RuneCountInString(title)) + "\n")
		for i, it := range its {
			num := fmt.Sprintf("%d. ", i+1)
			ind := strings.Repeat(" ", len(num))
			writeWrapped(&b, num+itemHead(it), w, "", ind)
			if it.Why != "" {
				label := "Why: "
				if status == StatusAttention || status == StatusSkipped {
					label = ""
				}
				writeWrapped(&b, label+it.Why, w, ind, ind+strings.Repeat(" ", len(label)))
			}
			if it.Todo != "" {
				writeWrapped(&b, "To do: "+it.Todo, w, ind, ind+"       ")
			}
		}
	}
	list("Needs a look (imported)", StatusAttention)
	list("Not imported", StatusLeftOut)
	r.writeSkipped(&b, w)
	if r.count("", StatusMerged) > 0 {
		title := "Duplicates (imported once)"
		b.WriteString("\n" + title + "\n" + strings.Repeat("-", len(title)) + "\n")
		for _, it := range r.Items {
			if it.Status == StatusMerged {
				writeWrapped(&b, "- "+it.Source+" is the same check as "+it.Name, w, "", "  ")
			}
		}
	}
	if len(r.Info) > 0 || len(o.Extra) > 0 {
		b.WriteString("\nGood to know\n------------\n")
		for _, s := range append(append([]string{}, r.Info...), o.Extra...) {
			writeWrapped(&b, "- "+s, w, "", "  ")
		}
	}
	return b.String()
}

func (r *Result) summaryRows() [][2]string {
	att := r.count("", StatusAttention)
	left := r.count("", StatusLeftOut)
	skipped := r.count("", StatusSkipped)
	rows := [][2]string{{"Imported:", r.summaryLine()}}
	if att > 0 {
		rows = append(rows, [2]string{"Needs a look:", plural(att, "item", "items") + " (imported; see below)"})
	} else {
		rows = append(rows, [2]string{"Needs a look:", "0"})
	}
	if left > 0 {
		rows = append(rows, [2]string{"Not imported:", plural(left, "item", "items") + " (see below for what to do)"})
	} else {
		rows = append(rows, [2]string{"Not imported:", "0"})
	}
	if skipped > 0 {
		rows = append(rows, [2]string{"Already here:", plural(skipped, "item", "items") + " (left as they are)"})
	}
	if merged := r.count("", StatusMerged); merged > 0 {
		rows = append(rows, [2]string{"Duplicates:", plural(merged, "check", "checks") + " found twice (imported once)"})
	}
	return rows
}

func itemHead(it Item) string {
	s := it.Source
	if strings.HasSuffix(it.Kind, "s") { // several of a kind in one item
		return capitalize(it.Kind) + ": " + it.Source
	}
	if it.Kind != "monitor" {
		s = capitalize(it.Kind) + " " + quoteName(it.Source)
	}
	if it.Type != "" && it.Kind == "monitor" {
		s += " (" + it.Type + ")"
	}
	if it.Name != "" && it.Name != it.Source && it.Status != StatusLeftOut {
		s += " → " + it.Name
	}
	return s
}

func quoteName(s string) string {
	if s == "" {
		return ""
	}
	return "\"" + s + "\""
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (r *Result) monitorRows() [][]string {
	var rows [][]string
	for _, it := range r.inFileOrder() {
		if it.Kind == "monitor" && (it.Status == StatusImported || it.Status == StatusAttention) {
			name := it.Name
			if it.Status == StatusAttention {
				name += " *"
			}
			rows = append(rows, []string{name, it.Type, it.Target, it.Group})
		}
	}
	return rows
}

// inFileOrder is the items in the order of their records in the file;
// those without a record (accounts) last.
func (r *Result) inFileOrder() []Item {
	pos := map[string]int{}
	for i, rec := range r.Records {
		pos[rec.Name] = i
	}
	at := func(it Item) int {
		if p, ok := pos[it.Name]; ok && it.Name != "" {
			return p
		}
		return len(pos)
	}
	items := append([]Item{}, r.Items...)
	sort.SliceStable(items, func(i, j int) bool { return at(items[i]) < at(items[j]) })
	return items
}

func (r *Result) otherRows() [][]string {
	var rows [][]string
	for _, it := range r.inFileOrder() {
		if it.Kind != "monitor" && it.Kind != "script checks" && (it.Status == StatusImported || it.Status == StatusAttention) {
			what := it.Kind
			if it.Type != "" {
				what += " (" + it.Type + ")"
			}
			name := it.Name
			if name == "" {
				name = it.Source
			}
			if it.Status == StatusAttention {
				name += " *"
			}
			rows = append(rows, []string{name, what, it.Target})
		}
	}
	return rows
}

// table lays rows out in columns that fit width: the widest column is
// shortened first; when even that is not enough each row is written as a
// short paragraph instead.
func table(head []string, rows [][]string, width int) string {
	n := len(head)
	used := make([]bool, n)
	for _, row := range rows {
		for i, c := range row {
			if c != "" {
				used[i] = true
			}
		}
	}
	used[0] = true
	var cols []int
	for i := range head {
		if used[i] {
			cols = append(cols, i)
		}
	}
	w := map[int]int{}
	for _, i := range cols {
		w[i] = utf8.RuneCountInString(head[i])
		for _, row := range rows {
			w[i] = max(w[i], utf8.RuneCountInString(row[i]))
		}
	}
	total := func() int {
		t := 2
		for _, i := range cols {
			t += w[i] + 2
		}
		return t - 2
	}
	// Shrink the widest column first; the name only when the others
	// are short already.
	for total() > width {
		widest, wi := -1, 0
		for _, i := range cols[1:] {
			if w[i] > wi && w[i] > 16 {
				widest, wi = i, w[i]
			}
		}
		if widest < 0 {
			for _, i := range cols {
				if w[i] > wi {
					widest, wi = i, w[i]
				}
			}
		}
		if widest < 0 || wi <= 12 {
			break
		}
		floor := 12
		if widest != cols[0] {
			floor = 16
		}
		w[widest] = max(floor, wi-(total()-width))
		if w[widest] == wi {
			break
		}
	}
	var b strings.Builder
	if total() > width {
		// Too narrow for columns: one short paragraph per row.
		for _, row := range rows {
			parts := []string{}
			for _, i := range cols[1:] {
				if row[i] != "" {
					parts = append(parts, row[i])
				}
			}
			writeWrapped(&b, "  "+row[0], width, "", "    ")
			if len(parts) > 0 {
				writeWrapped(&b, strings.Join(parts, " · "), width, "    ", "    ")
			}
		}
		return b.String()
	}
	line := func(cells []string) {
		var l strings.Builder
		l.WriteString("  ")
		for k, i := range cols {
			c := fit(cells[i], w[i])
			l.WriteString(c)
			if k < len(cols)-1 {
				l.WriteString(strings.Repeat(" ", w[i]-utf8.RuneCountInString(c)+2))
			}
		}
		b.WriteString(strings.TrimRight(l.String(), " ") + "\n")
	}
	line(head)
	dashes := make([]string, n)
	for _, i := range cols {
		dashes[i] = strings.Repeat("-", w[i])
	}
	line(dashes)
	for _, row := range rows {
		line(row)
	}
	return b.String()
}

// fit shortens s to n runes, ending in "…".
func fit(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// writeWrapped writes text wrapped at width, the first line after first,
// the others after rest.
func writeWrapped(b *strings.Builder, text string, width int, first, rest string) {
	prefix := first
	line := ""
	flush := func() {
		b.WriteString(strings.TrimRight(prefix+line, " ") + "\n")
		prefix, line = rest, ""
	}
	for _, word := range strings.Split(text, " ") {
		if word == "" && line != "" {
			line += " "
			continue
		}
		if line != "" && utf8.RuneCountInString(prefix+line+" "+word) > width {
			flush()
		}
		if line == "" {
			line = word
		} else {
			line += " " + word
		}
	}
	flush()
}

// Markdown is the same report for a file: summary, tables and lists.
func (r *Result) Markdown(o ReportOptions) string {
	var b strings.Builder
	b.WriteString("# Migration report: " + r.Source + " → Unagikuma\n\n")
	if o.DryRun {
		b.WriteString("> Preview: nothing was changed yet.\n\n")
	}
	fmt.Fprintf(&b, "%s, %s.\n\n", r.When.Format("2006-01-02 15:04"), mdEsc(r.File))
	b.WriteString("| | |\n|---|---|\n")
	for _, row := range r.summaryRows() {
		fmt.Fprintf(&b, "| %s | %s |\n", strings.TrimSuffix(row[0], ":"), mdEsc(row[1]))
	}
	if o.Config != "" {
		verb := "Added to"
		if o.DryRun {
			verb = "Would be added to"
		}
		fmt.Fprintf(&b, "\n%s `%s`.\n", verb, o.Config)
	}
	mdTable := func(title string, head []string, rows [][]string) {
		if len(rows) == 0 {
			return
		}
		b.WriteString("\n## " + title + "\n\n| " + strings.Join(head, " | ") + " |\n|" + strings.Repeat("---|", len(head)) + "\n")
		for _, row := range rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = mdEsc(c)
			}
			b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		}
	}
	mdTable("Monitors", []string{"Name", "Type", "Checks", "Group"}, r.monitorRows())
	mdTable("Also imported", []string{"Name", "What", "Details"}, r.otherRows())
	if r.count("", StatusAttention) > 0 {
		b.WriteString("\n_* needs a look: see below._\n")
	}
	list := func(title, status string) {
		first := true
		i := 0
		for _, it := range r.Items {
			if it.Status != status {
				continue
			}
			if first {
				b.WriteString("\n## " + title + "\n\n")
				first = false
			}
			i++
			fmt.Fprintf(&b, "%d. **%s**", i, mdEsc(itemHead(it)))
			if it.Why != "" {
				label := "Why: "
				if status != StatusLeftOut {
					label = ""
				}
				fmt.Fprintf(&b, "  \n   %s%s", label, mdEsc(it.Why))
			}
			if it.Todo != "" {
				fmt.Fprintf(&b, "  \n   To do: %s", mdEsc(it.Todo))
			}
			b.WriteString("\n")
		}
	}
	list("Needs a look (imported)", StatusAttention)
	list("Not imported", StatusLeftOut)
	list("Already here (left as they are)", StatusSkipped)
	list("Duplicates (imported once)", StatusMerged)
	if len(r.Info) > 0 || len(o.Extra) > 0 {
		b.WriteString("\n## Good to know\n\n")
		for _, s := range append(append([]string{}, r.Info...), o.Extra...) {
			b.WriteString("- " + mdEsc(s) + "\n")
		}
	}
	return b.String()
}

func mdEsc(s string) string {
	return strings.NewReplacer("|", `\|`, "*", `\*`, "_", `\_`, "<", "&lt;", "`", "'").Replace(s)
}

// humanDur writes seconds the short way: 90s, 5m, 2h, 1d.
func humanDur(sec int) string {
	switch {
	case sec <= 0:
		return "0s"
	case sec%86400 == 0:
		return strconv.Itoa(sec/86400) + "d"
	case sec%3600 == 0:
		return strconv.Itoa(sec/3600) + "h"
	case sec%60 == 0:
		return strconv.Itoa(sec/60) + "m"
	}
	return strconv.Itoa(sec) + "s"
}

// optOrder is where an option goes on a record: what the record is called
// first, then what it checks, how often, how it is grouped and who is
// notified; long free text last.
var optOrder = map[string]int{"title": 0, "retry": 20, "attempts": 21, "timeout": 22, "tags": 30, "device": 31, "group": 32, "parent": 33,
	"handler": 34, "disabled": 40, "policy": 41, "description": 50}

// sortOpts orders options stably by optOrder; others (what the check
// does) keep their order between the title and the timing.
func sortOpts(opts []config.Opt) {
	rank := func(k string) int {
		if r, ok := optOrder[k]; ok {
			return r
		}
		return 10
	}
	sort.SliceStable(opts, func(i, j int) bool { return rank(opts[i].Key) < rank(opts[j].Key) })
}

// sortSection orders a section's records by name.
func sortSection(recs []config.RecordSpec) {
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
}

var _ = time.Now

// writeSkipped lists what is already here by kind, names only: on a
// repeated import that is most of the input. The saved Markdown report
// has the details.
func (r *Result) writeSkipped(b *strings.Builder, w int) {
	byKind := map[string][]string{}
	seen := map[string]bool{}
	var kinds []string
	for _, it := range r.Items {
		if it.Status != StatusSkipped {
			continue
		}
		k := kindPlural(it.Kind)
		if _, ok := byKind[k]; !ok {
			kinds = append(kinds, k)
		}
		name := it.Name
		if name == "" {
			name = it.Source
		}
		if !seen[k+"\x00"+name] {
			seen[k+"\x00"+name] = true
			byKind[k] = append(byKind[k], name)
		}
	}
	if len(kinds) == 0 {
		return
	}
	title := "Already here (left as they are)"
	b.WriteString("\n" + title + "\n" + strings.Repeat("-", utf8.RuneCountInString(title)) + "\n")
	for _, k := range kinds {
		head := fmt.Sprintf("%s (%d): ", k, len(byKind[k]))
		writeWrapped(b, head+strings.Join(byKind[k], ", "), w, "", "  ")
	}
	writeWrapped(b, "They match what is already configured, so nothing was changed for them.", w, "", "")
}

func kindPlural(kind string) string {
	switch kind {
	case "monitor":
		return "Monitors"
	case "channel":
		return "Channels"
	case "policy":
		return "Policies"
	case "maintenance":
		return "Maintenance windows"
	case "status page":
		return "Status pages"
	case "account":
		return "Accounts"
	case "time period":
		return "Time periods"
	case "contact group":
		return "Contact groups"
	}
	if kind == "" {
		return "Other"
	}
	return strings.ToUpper(kind[:1]) + kind[1:] + "s"
}

// typeLabel names a record type for people.
func typeLabel(t string) string {
	if l, ok := map[string]string{"HTTP": "HTTP", "TCP": "TCP port", "PING": "Ping", "UDP": "UDP", "DNS": "DNS", "NTP": "NTP",
		"SNMP": "SNMP", "PUSH": "Push", "EXEC": "Script", "REDIS": "Redis", "POSTGRES": "PostgreSQL", "MYSQL": "MySQL",
		"GRPC": "gRPC", "DOCKER": "Docker", "DOMAIN": "Domain", "SYSLOG": "Syslog"}[t]; ok {
		return l
	}
	return t
}
