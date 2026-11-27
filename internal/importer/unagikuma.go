package importer

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// Unagikuma merges the records of another Unagikuma's monitors.conf into
// this installation. What it does with each record:
//
//   - a name not used here: imported as it is;
//   - the same record already here: left as it is;
//   - a monitor whose name is taken by a different record: imported under
//     a free name (-2, -3 …), and the parent= of the monitors imported
//     with it follows;
//   - a channel, period, team or status page whose name is taken: the one
//     here is kept (monitors name them, so a second copy under another
//     name would be wrong), and the report says so;
//   - the gateway monitors every installation adds for itself: left out.
//
// A file's own defaults (interval, retry, timeout, attempts) are written
// into the imported monitors when they differ from this file's, so a
// monitor keeps the timing it had. Secrets are not in monitors.conf: a
// ${secret:NAME} that does not exist here blocks the import until the
// person adds it (a configuration naming a missing secret is refused).
// Includes, and the directives that change how other records read ($SET,
// $DEFAULT, $TAG, $QUORUM, $DIALECT), are not carried: the report names
// them.
func Unagikuma(data []byte, existing *config.Config, o Options) (*Result, error) {
	f, cfg, err := loadUnagikuma(data)
	if err != nil {
		return nil, err
	}
	u := newUKImport(cfg, existing, o)
	// Pass 1: decide what happens to every record, and the new names.
	for _, rec := range f.Records() {
		u.decide(rec)
	}
	// Pass 2: write the records.
	for _, p := range u.picks {
		u.write(p)
	}
	u.reportMissingSecrets()
	u.notes(data)
	u.r.finish()
	return u.r, nil
}

// loadUnagikuma reads an uploaded monitors.conf; one that does not load
// is refused with its first errors.
func loadUnagikuma(data []byte) (*config.File, *config.Config, error) {
	if len(data) == 0 {
		return nil, nil, errors.New("the file is empty")
	}
	if includeLine.Match(data) {
		return nil, nil, errors.New("$INCLUDE is not followed: put the records into one file and import that")
	}
	f, cfg, diags := config.Load("upload", data, config.Options{})
	if config.HasErrors(diags) {
		var msgs []string
		for _, d := range diags {
			if d.Severity == config.SevError && len(msgs) < 3 {
				msgs = append(msgs, d.Error())
			}
		}
		return nil, nil, fmt.Errorf("this is not a valid monitors.conf: %s", strings.Join(msgs, "; "))
	}
	return f, cfg, nil
}

// ukImport is one import of a monitors.conf.
type ukImport struct {
	r          *Result
	o          Options
	here       map[string]config.Record // records here, by name (nil: none given)
	hereChecks map[string]string        // a monitor's effect → its name here
	taken      map[string]bool          // names used here
	src, dst   config.Defaults          // the file's defaults and the ones here
	picks      []ukPick
	rename     map[string]string // a record's name in the file → its name here
	used       map[string]bool   // names given to imported records
	missing    map[string][]string
}

// ukPick is a record that will be written, under name.
type ukPick struct {
	rec  config.Record
	kind string
	name string
	item int
}

func newUKImport(cfg, existing *config.Config, o Options) *ukImport {
	u := &ukImport{
		r:          &Result{Source: "Unagikuma", File: o.File, Area: o.Area, When: o.now(), Secrets: map[string]string{}, totalNow: o.TotalNow, totalLimit: o.TotalLimit},
		o:          o,
		hereChecks: map[string]string{},
		taken:      map[string]bool{},
		src:        cfg.Defaults,
		dst:        cfg.Defaults,
		rename:     map[string]string{},
		used:       map[string]bool{},
		missing:    map[string][]string{},
	}
	if o.Existing != nil {
		u.here = map[string]config.Record{}
		for _, rec := range o.Existing.Records() {
			u.here[rec.Name] = rec
			u.taken[rec.Name] = true
		}
	}
	if existing != nil {
		for _, m := range existing.Monitors {
			u.taken[m.Name] = true
		}
		u.dst = existing.Defaults
	}
	for name, rec := range u.here {
		if config.IsMonitorType(rec.Type) {
			k := canon(rec, u.dst, true)
			if prev, ok := u.hereChecks[k]; !ok || name < prev {
				u.hereChecks[k] = name
			}
		}
	}
	return u
}

// decide reports what happens to rec and, when it is imported, picks it
// with the name it gets here.
func (u *ukImport) decide(rec config.Record) {
	kind, ok := recordKinds[rec.Type]
	if !ok && config.IsMonitorType(rec.Type) {
		kind, ok = "monitor", true
	}
	it := Item{Kind: kind, Source: rec.Name, Type: rec.Type, Target: rec.Target, Status: StatusImported}
	switch {
	case !ok:
		it.Kind, it.Status, it.Why = "record", StatusLeftOut, "a record of a kind this import does not know"
		u.r.add(it)
		return
	case kind == "monitor" && autoMonitors[rec.Name]:
		it.Status, it.Why = StatusLeftOut, "every installation adds its own gateway monitors"
		u.r.add(it)
		return
	}
	name := rec.Name
	key := canon(rec, u.src, kind == "monitor")
	if kind == "monitor" {
		// The same check, under whatever name it has here: imported
		// before. The monitors that name it as their parent follow it.
		if n, ok := u.hereChecks[key]; ok {
			it.Name, it.Status, it.Why = n, StatusSkipped, "already here"
			if n != rec.Name {
				it.Why = "already here as " + n
				u.rename[rec.Name] = n
			}
			u.r.add(it)
			return
		}
	}
	if old, clash := u.here[name]; clash || u.taken[name] || u.used[name] {
		if clash && kind != "monitor" && canon(old, u.dst, false) == key {
			it.Name, it.Status, it.Why = name, StatusSkipped, "already here"
			u.r.add(it)
			return
		}
		if kind != "monitor" {
			it.Name, it.Status = name, StatusSkipped
			it.Why = "a " + kind + " of this name is already here and differs; the one here is kept"
			it.Todo = "Rename one of them and import again to have both."
			u.r.add(it)
			return
		}
		name = freeName(rec.Name, u.taken, u.used)
		u.rename[rec.Name] = name
		it.attention("A different record of this name is already here; imported as "+name+".", "")
	}
	u.used[name] = true
	it.Name = name
	u.r.add(it)
	u.picks = append(u.picks, ukPick{rec: rec, kind: kind, name: name, item: len(u.r.Items) - 1})
}

// write adds a picked record to the result.
func (u *ukImport) write(p ukPick) {
	spec := config.RecordSpec{Name: p.name, Type: p.rec.Type, Target: p.rec.Target, Interval: p.rec.Interval}
	has := map[string]bool{}
	it := &u.r.Items[p.item]
	for _, op := range p.rec.Opts {
		has[op.Key] = true
		v := op.Value
		// A policy that already exists here receives these monitors'
		// alerts: its channels, not the other file's.
		if p.kind == "monitor" && op.Key == "policy" && u.here[v].Name != "" && !u.used[v] {
			it.attention("Its alerts go to the policy "+v+" that is already here, and to its channels.", "")
		}
		if op.Key == "parent" && p.kind == "monitor" {
			v = remapList(v, u.rename)
		}
		spec.Opts = append(spec.Opts, config.Opt{Key: op.Key, Value: v})
		u.secretsOf(p, it, v)
	}
	if p.kind == "monitor" {
		u.carryDefaults(&spec, has)
		u.r.Monitors++
	}
	title, shared := sectionOf(p.kind)
	if p.kind == "channel" {
		u.r.Channels++
	}
	u.r.addToSection(title, shared, spec)
}

// secretsOf notes the secrets an option value names: missing here, or
// here and about to be sent where the record points.
func (u *ukImport) secretsOf(p ukPick, it *Item, v string) {
	for _, m := range secretRef.FindAllStringSubmatch(v, -1) {
		if u.o.SecretExists == nil || !u.o.SecretExists(m[1]) {
			u.missing[m[1]] = appendOnce(u.missing[m[1]], p.name)
			continue
		}
		// The secret here has the same name, perhaps not the same
		// purpose: its value goes wherever this record points.
		where := p.rec.Target
		if where == "" {
			where = "where the record points"
		}
		it.attention("It uses the secret "+m[1]+" of this installation: its value will be sent to "+where+".", "Check the address before importing, or remove the record from the file.")
	}
}

// carryDefaults writes the file's own defaults into a monitor where they
// differ from the ones here, so it keeps the timing it had.
func (u *ukImport) carryDefaults(spec *config.RecordSpec, has map[string]bool) {
	src, dst := u.src, u.dst
	if spec.Interval == "" && src.Interval != dst.Interval && src.Interval > 0 {
		spec.Interval = humanDur(int(src.Interval.Seconds()))
	}
	for _, d := range []struct {
		key string
		v   string
		ne  bool
	}{
		{"retry", humanDur(int(src.Retry.Seconds())), src.Retry != dst.Retry && src.Retry > 0},
		{"timeout", humanDur(int(src.Timeout.Seconds())), src.Timeout != dst.Timeout && src.Timeout > 0},
		{"attempts", strconv.Itoa(src.Attempts), src.Attempts != dst.Attempts && src.Attempts > 0},
	} {
		if d.ne && !has[d.key] {
			spec.Opts = append(spec.Opts, config.Opt{Key: d.key, Value: d.v})
		}
	}
}

// reportMissingSecrets lists the secrets the records need that are not here.
func (u *ukImport) reportMissingSecrets() {
	names := make([]string, 0, len(u.missing))
	for n := range u.missing {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		u.r.add(Item{Kind: "secret", Source: n, Status: StatusLeftOut,
			Why:  "used by " + strings.Join(u.missing[n], ", ") + ", and not defined here (secret values are not in monitors.conf)",
			Todo: "Add the secret " + n + " under Advanced › Secrets first: a configuration that names a missing secret is refused, so the import waits until it exists."})
	}
}

// notes says what of the file is not carried over.
func (u *ukImport) notes(data []byte) {
	if dirs := directivesIn(data); len(dirs) > 0 {
		u.r.Info = append(u.r.Info, "Not carried over: "+strings.Join(dirs, ", ")+". Records that rely on them were written as they stand; check the preview.")
	}
	if u.o.Area != "" {
		u.r.Info = append(u.r.Info, "Sections ($SCOPE) of the file are not kept: the monitors go to "+u.o.Area+".")
	} else if scopeLine.Match(data) {
		u.r.Info = append(u.r.Info, "Sections ($SCOPE) of the file are not kept: the monitors go to the general section. Choose a section to put them elsewhere.")
	}
}

// recordKinds names the records that are not monitors.
var recordKinds = map[string]string{"CHANNEL": "channel", "PERIOD": "period", "STATUSPAGE": "status page"}

// autoMonitors are added by every installation for its own gateways.
var autoMonitors = map[string]bool{"lan-gateway": true, "internet-gateway": true}

var (
	includeLine = regexp.MustCompile(`(?mi)^\s*\$INCLUDE\b`)
	scopeLine   = regexp.MustCompile(`(?mi)^\s*\$SCOPE\b`)
	secretRef   = regexp.MustCompile(`\$\{secret:([A-Za-z0-9_.-]+)\}`)
	notCarried  = regexp.MustCompile(`(?mi)^\s*\$(SET|DEFAULT|TAG|QUORUM|DIALECT)\b`)
)

func directivesIn(data []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range notCarried.FindAllSubmatch(data, -1) {
		d := "$" + strings.ToUpper(string(m[1]))
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// sectionOf titles the block a kind of record goes into.
func sectionOf(kind string) (title string, shared bool) {
	switch kind {
	case "monitor":
		return "Monitors", false
	case "channel":
		return "Channels", true
	case "period":
		return "Periods", true
	}
	return "Status pages", true
}

// addToSection appends a record to the section of that title, in the
// order the sections first appear.
func (r *Result) addToSection(title string, shared bool, rec config.RecordSpec) {
	for i := range r.sections {
		if r.sections[i].title == title {
			r.sections[i].recs = append(r.sections[i].recs, rec)
			return
		}
	}
	r.sections = append(r.sections, section{title: title, shared: shared, recs: []config.RecordSpec{rec}})
}

// canon is what a record does, not how it is written: for a monitor its
// type, target and options with the file's own defaults filled in (an
// interval, retry, timeout or attempts left out is the file's default) and
// without parent=, which follows the renames; for the others their type,
// target and options.
func canon(r config.Record, d config.Defaults, monitor bool) string {
	opts := map[string]string{}
	for _, o := range r.Opts {
		opts[o.Key] = o.Value
	}
	iv := ""
	if monitor {
		delete(opts, "parent")
		fill := func(k string, def string, ok bool) {
			if _, has := opts[k]; !has && ok {
				opts[k] = def
			}
		}
		fill("retry", humanDur(int(d.Retry.Seconds())), d.Retry > 0)
		fill("timeout", humanDur(int(d.Timeout.Seconds())), d.Timeout > 0)
		fill("attempts", strconv.Itoa(d.Attempts), d.Attempts > 0)
		iv = r.Interval
		if iv == "" && d.Interval > 0 {
			iv = humanDur(int(d.Interval.Seconds()))
		}
		for _, k := range []string{"retry", "timeout"} {
			opts[k] = normDur(opts[k])
		}
		iv = normDur(iv)
	}
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(r.Type + "|" + r.Target + "|" + iv)
	for _, k := range keys {
		b.WriteString("|" + k + "=" + opts[k])
	}
	return b.String()
}

// normDur writes a duration the short way, so 60s and 1m are the same.
func normDur(v string) string {
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return humanDur(int(d.Seconds()))
	}
	return v
}

// freeName finds name-2, name-3 … not used here or by an earlier record.
func freeName(base string, taken, used map[string]bool) string {
	for i := 2; ; i++ {
		n := base + "-" + strconv.Itoa(i)
		if !taken[n] && !used[n] {
			return n
		}
	}
}

// remapList renames the entries of a comma-separated list.
func remapList(v string, rename map[string]string) string {
	if len(rename) == 0 {
		return v
	}
	parts := strings.Split(v, ",")
	for i, p := range parts {
		if n, ok := rename[strings.TrimSpace(p)]; ok {
			parts[i] = n
		}
	}
	return strings.Join(parts, ",")
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
