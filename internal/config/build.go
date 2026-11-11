package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/keywords"
	"github.com/githubflyideas/unagikuma/internal/schedule"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Options control validation.
type Options struct {
	// AllowEnv lists ${env:X} names allowed beyond the UNAGIKUMA_ prefix.
	AllowEnv []string
	// SecretKnown reports whether a secret exists (nil = don't check).
	SecretKnown func(name string) bool
	// ValidateMonitor checks type-specific options (the probe package).
	ValidateMonitor func(m *Monitor) error
	// ValidateChannel checks channel options (the notify package).
	ValidateChannel func(kind string, opts map[string]string) error
	// Location reads the times of maintenance windows (default local).
	Location *time.Location
}

// Change classes of option keys (hot reload decides what to reset).
// Names kept free for later versions, so a file written for one of them
// says it needs a newer release instead of failing as a syntax error.
var (
	reservedTypes      = map[string]bool{"SCHEDULE": true, "ROTA": true, "ROUTE": true, "TEMPLATE": true, "CHANGE": true, "PERSON": true}
	reservedDirectives = map[string]bool{"TIMEZONE": true, "HOLIDAYS": true}
)

// was= names the monitor's previous name: written by hand when renaming
// in the file, it carries the history over (and does nothing afterwards).
var metaOpts = map[string]bool{"title": true, "description": true, "tags": true, "group": true, "device": true, "handler": true, "was": true, "tenant": true}
var paramOpts = map[string]bool{
	"interval": true, "retry": true, "timeout": true, "attempts": true, "recover-attempts": true,
	"degraded": true, "quorum": true, "on-insufficient": true, "on-filtered": true, "on-node-loss": true,
	"scope": true, "policy": true, "partial-escalate": true, "disabled": true, "grace": true,
	"parent": true, "nodes": true, "share": true, "team": true, "region": true, "mute": true, "system-channel": true,
}

var refRe = regexp.MustCompile(`\$\{(secret|env):([A-Za-z0-9_.-]+)\}`)

// secretKeys are option keys whose values are credentials.
var secretKeys = map[string]bool{"access-key-secret": true, "secret-key": true, "auth-token": true, "token": true, "secret": true, "password": true, "community": true, "sendkey": true, "key": true, "apikey": true, "api-key": true, "routing-key": true, "auth": true}

// webhookKinds embed a credential in their URL.
var webhookKinds = map[string]bool{"feishu": true, "dingtalk": true, "wecom": true, "slack": true, "discord": true, "teams": true, "googlechat": true, "mattermost": true}

type builder struct {
	f     *File
	opt   Options
	cfg   *Config
	diags []Diagnostic
	names map[string]Pos
}

// legacyRotationOpts are the options of on-call rotations, a feature that
// was removed: they are accepted and ignored so old files stay valid.
var legacyRotationOpts = map[string]bool{"rotate": true, "handoff": true, "start": true, "tz": true}

func (b *builder) errf(p Pos, code, format string, args ...any) {
	b.diags = append(b.diags, Diagnostic{Pos: p, Severity: SevError, Code: code, Msg: fmt.Sprintf(format, args...)})
}

func (b *builder) warnf(p Pos, code, format string, args ...any) {
	b.diags = append(b.diags, Diagnostic{Pos: p, Severity: SevWarning, Code: code, Msg: fmt.Sprintf(format, args...)})
}

// Config builds and validates the semantic model. The model is returned
// even when there are errors (for editors); callers must not apply a
// configuration with error diagnostics.
func (f *File) Config(opt Options) (*Config, []Diagnostic) {
	b := &builder{f: f, opt: opt, cfg: &Config{Tags: map[string]TagMeta{}, Quorum: "majority", Dialect: keywords.DialectAuto}, names: map[string]Pos{}}
	b.build()
	sort.SliceStable(b.diags, func(i, j int) bool {
		if b.diags[i].Pos.Line != b.diags[j].Pos.Line {
			return b.diags[i].Pos.Line < b.diags[j].Pos.Line
		}
		return b.diags[i].Pos.Col < b.diags[j].Pos.Col
	})
	return b.cfg, b.diags
}

// Load parses and validates in one step.
func Load(path string, src []byte, opt Options) (*File, *Config, []Diagnostic) {
	f, diags := Parse(path, src)
	cfg, d2 := f.Config(opt)
	return f, cfg, append(diags, d2...)
}

func (b *builder) build() {
	var (
		soaSeen      bool
		recordSeen   bool
		globalOnce   = map[string]Pos{}
		defaults     []Opt
		setOverrides = map[string]string{}
		setPos       = map[string]Pos{}
		monitorRecs  []monitorRec
		scope        *scopeDir
		later        []Record // NODE and POLICY: built once $SET is known
		maintRecs    []Record // MAINT: built once the monitors are known
	)
	for i := range b.f.Stmts {
		s := &b.f.Stmts[i]
		if len(s.Tokens) == 0 {
			continue
		}
		if s.isDirective() {
			d := s.directive()
			pos := s.Tokens[0].Pos
			// $VERSION may come first: it says how to read the rest.
			if !soaSeen && d.Name != "VERSION" && d.Name != "FILE" {
				b.errf(pos, "soa_first", "the file header ($FILE) must come first")
			}
			switch d.Name {
			case "FILE":
				if soaSeen {
					b.errf(pos, "duplicate_soa", "only one file header ($FILE or SOA) is allowed")
					continue
				}
				if recordSeen {
					b.errf(pos, "soa_first", "the file header ($FILE) must come first")
				}
				soaSeen = true
				b.fileHeader(d)
			case "QUORUM", "DIALECT":
				if p, dup := globalOnce[d.Name]; dup {
					b.errf(pos, "duplicate_directive", "$%s already set at line %d", d.Name, p.Line)
					continue
				}
				globalOnce[d.Name] = pos
				if recordSeen {
					b.errf(pos, "directive_order", "$%s must come before the first record", d.Name)
				}
				if len(d.Args) != 1 {
					b.errf(pos, "syntax", "$%s takes exactly one value", d.Name)
					continue
				}
				v := d.Args[0].Text
				if d.Name == "QUORUM" {
					if err := ValidateQuorum(v); err != nil {
						b.errf(d.Args[0].Pos, "invalid_value", "%v", err)
					}
					b.cfg.Quorum = v
				} else {
					dl, err := keywords.ParseDialect(v)
					if err != nil {
						b.errf(d.Args[0].Pos, "invalid_value", "%v", err)
					}
					b.cfg.Dialect = dl
				}
			case "SET":
				for _, t := range d.Args {
					k, v, ok := t.option()
					if !ok {
						b.errf(t.Pos, "syntax", "$SET expects key=value")
						continue
					}
					if p, dup := setPos[k]; dup {
						b.errf(t.Pos, "duplicate_tunable", "%s already set at line %d", k, p.Line)
						continue
					}
					if _, err := tunables.New(map[string]string{k: v}); err != nil {
						b.errf(t.Pos, "invalid_tunable", "%v", err)
						continue
					}
					setOverrides[k], setPos[k] = v, t.Pos
				}
			case "TAG":
				b.tagDirective(d)
			case "DEFAULT":
				defaults = nil
				for _, t := range d.Args {
					k, v, ok := t.option()
					if !ok {
						b.errf(t.Pos, "syntax", "$DEFAULT expects key=value")
						continue
					}
					defaults = append(defaults, Opt{Key: k, Value: v, Pos: t.Pos})
				}
			case "SCOPE":
				scope = b.scopeDirective(d)
			case "VERSION":
				// The syntax version this file is written in; files without
				// it are version 1.
				if p, dup := globalOnce[d.Name]; dup {
					b.errf(pos, "duplicate_directive", "$%s already set at line %d", d.Name, p.Line)
					continue
				}
				globalOnce[d.Name] = pos
				if len(d.Args) != 1 || d.Args[0].Text != "1" {
					b.errf(pos, "unsupported_version", "this release reads monitors.conf version 1; the file needs a newer unagikuma")
				}
			case "INCLUDE":
				b.errf(pos, "unsupported", "$INCLUDE is not supported yet")
			default:
				if reservedDirectives[d.Name] {
					b.errf(pos, "needs_newer", "$%s is reserved for a later version of unagikuma", d.Name)
					continue
				}
				b.errf(pos, "unknown_directive", "unknown directive $%s", d.Name)
			}
			continue
		}
		r, diag := s.record()
		if diag != nil {
			b.diags = append(b.diags, *diag)
			continue
		}
		if r.Type == "SOA" {
			if soaSeen {
				b.errf(r.TypePos, "duplicate_soa", "only one file header ($FILE or SOA) is allowed")
				continue
			}
			if recordSeen {
				b.errf(r.TypePos, "soa_first", "the file header must come first")
			}
			soaSeen = true
			b.soa(r)
			continue
		}
		if !soaSeen && !recordSeen {
			b.errf(r.NamePos, "soa_first", "the file header ($FILE) must come first")
		}
		recordSeen = true
		if !ValidName(r.Name) {
			b.errf(r.NamePos, "invalid_name", "invalid name %q: letters, digits, '_', '.', '-'; must start with a letter or digit; at most 63 characters", r.Name)
			continue
		}
		if p, dup := b.names[r.Name]; dup {
			b.errf(r.NamePos, "duplicate_name", "name %q already used at line %d (names are unique across all record types)", r.Name, p.Line)
			continue
		}
		b.names[r.Name] = r.NamePos
		if dupKeys(r.Opts) != nil {
			o := dupKeys(r.Opts)
			b.errf(o.Pos, "duplicate_option", "option %s given twice", o.Key)
			continue
		}
		if r.Interval != "" && !IsMonitorType(r.Type) {
			b.warnf(r.Stmt.Tokens[1].Pos, "interval_ignored", "only monitors have an interval; %s ignores it", r.Type)
		}
		switch {
		case IsMonitorType(r.Type):
			monitorRecs = append(monitorRecs, monitorRec{r, append([]Opt(nil), defaults...), scope})
		case r.Type == "NODE", r.Type == "POLICY":
			later = append(later, r)
		case r.Type == "CHANNEL":
			b.channel(r)
		case r.Type == "STATUSPAGE":
			b.statusPage(r)
		case r.Type == "MAINT":
			maintRecs = append(maintRecs, r)
		case r.Type == "PERIOD":
			b.period(r)
		case r.Type == "TEAM":
			b.team(r)
		case reservedTypes[r.Type]:
			b.errf(r.TypePos, "needs_newer", "record type %s is reserved for a later version of unagikuma", r.Type)
		default:
			b.errf(r.TypePos, "unknown_type", "unknown record type %s", r.Type)
		}
	}
	if !soaSeen {
		b.errf(Pos{File: b.f.Path, Line: 1, Col: 1}, "missing_soa", "missing file header: start the file with $FILE revision=1")
	}
	ts, err := tunables.New(setOverrides)
	if err != nil {
		b.errf(Pos{File: b.f.Path, Line: 1, Col: 1}, "invalid_tunable", "%v", err)
	}
	b.cfg.Tunables = ts
	for _, r := range later {
		if r.Type == "NODE" {
			b.node(r)
		} else {
			b.policyRec(r)
		}
	}
	for _, mr := range monitorRecs {
		b.monitor(mr.r, mr.defaults, mr.scope)
	}
	for _, r := range maintRecs {
		b.maint(r)
	}
	b.implicitPolicies()
	b.crossRefs()
}

type monitorRec struct {
	r        Record
	defaults []Opt
	scope    *scopeDir
}

// scopeDir is an active $SCOPE: the nodes (or regions) that run the
// records after it, and the policy they notify by default.
type scopeDir struct {
	spec   string   // as written, e.g. "region:north" or "bj,sh"
	nodes  []string // node names and region:NAME entries
	policy string
	pos    Pos
}

// scopeDirective parses "$SCOPE <nodes|region:NAME|*> [policy=NAME]".
// "*" (or no argument) returns to records checked by every node.
func (b *builder) scopeDirective(d Directive) *scopeDir {
	pos := d.Stmt.Tokens[0].Pos
	if len(d.Args) == 0 {
		return nil
	}
	sc := &scopeDir{spec: d.Args[0].Text, pos: d.Args[0].Pos}
	for _, t := range d.Args[1:] {
		k, v, ok := t.option()
		if !ok || k != "policy" {
			b.errf(t.Pos, "syntax", "$SCOPE takes nodes or region:NAME, then optionally policy=NAME")
			continue
		}
		sc.policy = v
	}
	if sc.spec == "*" {
		if sc.policy != "" {
			b.errf(pos, "syntax", "$SCOPE * cannot set a policy; it returns to the global section")
		}
		return nil
	}
	for _, n := range strings.Split(sc.spec, ",") {
		if n = strings.TrimSpace(n); n != "" {
			sc.nodes = append(sc.nodes, n)
		}
	}
	return sc
}

// expandNodes turns node names and region:NAME entries into node names.
func (b *builder) expandNodes(entries []string, p Pos) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, e := range entries {
		region, ok := strings.CutPrefix(e, "region:")
		if !ok {
			add(e)
			continue
		}
		found := false
		for _, n := range b.cfg.Nodes {
			if n.Region == region {
				add(n.Name)
				found = true
			}
		}
		if !found {
			b.errf(p, "unknown_region", "no node is in region %q", region)
		}
	}
	return out
}

func dupKeys(opts []Opt) *Opt {
	seen := map[string]bool{}
	for i := range opts {
		if opts[i].Key == "" {
			continue
		}
		if seen[opts[i].Key] {
			return &opts[i]
		}
		seen[opts[i].Key] = true
	}
	return nil
}

func lines(s *Stmt) [2]int { return [2]int{s.StartLine, s.EndLine} }

// Header defaults: what a $FILE line leaves out.
var headerDefaults = Defaults{Interval: time.Minute, Retry: 15 * time.Second, Timeout: 5 * time.Second, Attempts: 3}

// fileHeader reads the file header:
//
//	$FILE revision=N [master=NAME] [admin=EMAIL] [interval=D] [retry=D] [timeout=D] [attempts=N]
//
// It holds what the older SOA record holds, by name; only the revision is
// required.
func (b *builder) fileHeader(d Directive) {
	b.cfg.MName, b.cfg.Defaults = "unagikuma", headerDefaults
	seen := map[string]bool{}
	var timeoutPos Pos
	for _, t := range d.Args {
		k, v, ok := t.option()
		if !ok {
			b.errf(t.Pos, "syntax", "$FILE takes options: revision=N, and optionally master=, admin=, interval=, retry=, timeout=, attempts=")
			continue
		}
		if seen[k] {
			b.errf(t.Pos, "duplicate_option", "option %s given twice", k)
			continue
		}
		seen[k] = true
		switch k {
		case "revision":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil || n == 0 {
				b.errf(t.Pos, "invalid_serial", "revision must be an integer between 1 and 4294967295")
			}
			b.cfg.Serial = uint32(n)
		case "master":
			if !ValidName(v) {
				b.errf(t.Pos, "invalid_name", "master: invalid node name %q", v)
				continue
			}
			b.cfg.MName = v
		case "admin":
			b.cfg.RName = v
		case "interval", "retry", "timeout":
			dur, err := tunables.ParseDuration(v)
			if err != nil || dur <= 0 {
				b.errf(t.Pos, "invalid_duration", "invalid duration %q", v)
				continue
			}
			switch k {
			case "interval":
				b.cfg.Defaults.Interval = dur
			case "retry":
				b.cfg.Defaults.Retry = dur
			default:
				b.cfg.Defaults.Timeout, timeoutPos = dur, t.Pos
			}
		case "attempts":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 100 {
				b.errf(t.Pos, "invalid_value", "attempts must be an integer between 1 and 100")
				continue
			}
			b.cfg.Defaults.Attempts = n
		default:
			b.errf(t.Pos, "unknown_option", "$FILE has no option %s (revision, master, admin, interval, retry, timeout, attempts)", k)
		}
	}
	if !seen["revision"] {
		b.errf(d.Stmt.Tokens[0].Pos, "invalid_serial", "$FILE needs revision=N (increase it after editing by hand)")
	}
	if df := b.cfg.Defaults; df.Timeout >= df.Interval {
		if timeoutPos.Line == 0 {
			timeoutPos = d.Stmt.Tokens[0].Pos
		}
		b.warnf(timeoutPos, "timeout_ge_interval", "timeout %s is not shorter than the interval %s", df.Timeout, df.Interval)
	}
}

func (b *builder) soa(r Record) {
	b.cfg.MName = strings.TrimSuffix(r.Target, ".")
	vals := r.Opts
	if len(vals) != 6 {
		b.errf(r.TypePos, "syntax", "SOA needs: mname rname ( serial interval retry timeout attempts )")
		b.cfg.Defaults = Defaults{Interval: time.Minute, Retry: 15 * time.Second, Timeout: 5 * time.Second, Attempts: 3}
		return
	}
	b.cfg.RName = rnameToEmail(vals[0].Value)
	serial, err := strconv.ParseUint(vals[1].Value, 10, 32)
	if err != nil || serial == 0 {
		b.errf(vals[1].Pos, "invalid_serial", "serial must be an integer between 1 and 4294967295")
	}
	b.cfg.Serial = uint32(serial)
	durs := make([]time.Duration, 3)
	for i := 0; i < 3; i++ {
		d, err := tunables.ParseDuration(vals[2+i].Value)
		if err != nil || d <= 0 {
			b.errf(vals[2+i].Pos, "invalid_duration", "invalid duration %q", vals[2+i].Value)
			d = []time.Duration{time.Minute, 15 * time.Second, 5 * time.Second}[i]
		}
		durs[i] = d
	}
	att, err := strconv.Atoi(vals[5].Value)
	if err != nil || att < 1 || att > 100 {
		b.errf(vals[5].Pos, "invalid_value", "attempts must be an integer between 1 and 100")
		att = 3
	}
	b.cfg.Defaults = Defaults{Interval: durs[0], Retry: durs[1], Timeout: durs[2], Attempts: att}
	if durs[2] >= durs[0] {
		b.warnf(vals[4].Pos, "timeout_ge_interval", "timeout %s is not shorter than the interval %s", durs[2], durs[0])
	}
}

// rnameToEmail turns a BIND RNAME ("ops.example.com.") into an e-mail.
func rnameToEmail(s string) string {
	s = strings.TrimSuffix(s, ".")
	if strings.Contains(s, "@") {
		return s
	}
	// The first unescaped dot separates the local part.
	var local strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			local.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == '.' {
			return local.String() + "@" + s[i+1:]
		}
		local.WriteByte(s[i])
	}
	return s
}

func (b *builder) tagDirective(d Directive) {
	if len(d.Args) == 0 || d.Args[0].Paren != 0 {
		b.errf(d.Stmt.Tokens[0].Pos, "syntax", "$TAG key[:value] color=#rrggbb [description=...]")
		return
	}
	key := d.Args[0].Text
	meta := TagMeta{}
	for _, t := range d.Args[1:] {
		k, v, ok := t.option()
		if !ok {
			b.errf(t.Pos, "syntax", "$TAG expects key=value options")
			continue
		}
		switch k {
		case "color":
			if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(v) {
				b.errf(t.Pos, "invalid_value", "color must be #rrggbb")
			}
			meta.Color = v
		case "description":
			meta.Description = v
		default:
			b.errf(t.Pos, "unknown_option", "unknown $TAG option %s", k)
		}
	}
	b.cfg.Tags[key] = meta
}

// ValidateQuorum checks a quorum spec: majority, all, N or N%.
func ValidateQuorum(s string) error {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case v == "majority" || v == "all":
		return nil
	case strings.HasSuffix(v, "%"):
		p, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
		if err != nil || !(p > 0 && p <= 100) { // also refuses NaN
			return fmt.Errorf("invalid quorum %q", s)
		}
		return nil
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid quorum %q (want majority, all, N or N%%)", s)
		}
		return nil
	}
}

func parseTags(s string) ([]Tag, error) {
	var out []Tag
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, ":")
		if k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("invalid tag %q", part)
		}
		out = append(out, Tag{Key: k, Value: v})
	}
	return out, nil
}

func parseBoolOpt(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "yes", "true", "on", "1":
		return true, nil
	case "no", "false", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("want yes or no, got %q", v)
}

// refs collects ${secret:} / ${env:} references and checks them.
func (b *builder) refs(p Pos, values map[string]string) []string {
	var out []string
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, m := range refRe.FindAllStringSubmatch(values[k], -1) {
			out = append(out, m[0])
			switch m[1] {
			case "env":
				if !strings.HasPrefix(m[2], "UNAGIKUMA_") && !contains(b.opt.AllowEnv, m[2]) {
					b.errf(p, "env_not_allowed", "${env:%s} is not allowed: only UNAGIKUMA_* variables or names passed with --allow-env", m[2])
				}
			case "secret":
				if b.opt.SecretKnown != nil && !b.opt.SecretKnown(m[2]) {
					b.errf(p, "unknown_secret", "secret %q does not exist (create it under Settings → Secrets)", m[2])
				}
			}
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// plaintext warns about credentials written in clear text.
func (b *builder) plaintext(p Pos, kind string, values map[string]string) {
	for k, v := range values {
		if v == "" || refRe.MatchString(v) {
			continue
		}
		if secretKeys[k] || (k == "url" && webhookKinds[kind]) {
			b.warnf(p, "plaintext_secret", "%s looks like a credential written in clear text; use ${secret:NAME}", k)
		}
	}
}

func (b *builder) monitor(r Record, defaults []Opt, scope *scopeDir) {
	m := &Monitor{Name: r.Name, Written: r.Type, Type: r.Type, Pos: r.NamePos, Lines: lines(r.Stmt), Opts: map[string]string{}}
	optPos := map[string]Pos{}
	for _, o := range defaults {
		m.Opts[o.Key], optPos[o.Key] = o.Value, o.Pos
	}
	for _, o := range r.Opts {
		m.Opts[o.Key], optPos[o.Key] = o.Value, o.Pos
	}
	pos := func(k string) Pos {
		if p, ok := optPos[k]; ok {
			return p
		}
		return r.NamePos
	}
	d := b.cfg.Defaults
	m.Interval, m.Retry, m.Timeout, m.Attempts = d.Interval, d.Retry, d.Timeout, d.Attempts
	m.RecoverAttempts = b.cfg.Tunables.Int("engine.recover-attempts")
	m.Title, m.Scope, m.Policy = r.Name, "auto", "default"
	m.Quorum, m.OnInsufficient, m.OnFiltered, m.OnNodeLoss = b.cfg.Quorum, "unknown", "fail", "degrade"
	m.PartialEscalate = b.cfg.Tunables.Duration("engine.partial-escalate")

	explicitInterval := r.Interval != ""
	if explicitInterval {
		iv, err := tunables.ParseDuration(r.Interval)
		if err != nil || iv <= 0 {
			b.errf(r.NamePos, "invalid_duration", "invalid interval %q", r.Interval)
		} else {
			m.Interval = iv
		}
	}
	dur := func(k string, dst *time.Duration, allowOff bool) {
		v, ok := m.Opts[k]
		if !ok {
			return
		}
		if allowOff && strings.EqualFold(v, "off") {
			*dst = 0
			return
		}
		x, err := tunables.ParseDuration(v)
		if err != nil || x < 0 {
			b.errf(pos(k), "invalid_duration", "%s: invalid duration %q", k, v)
			return
		}
		*dst = x
	}
	num := func(k string, dst *int, lo, hi int) {
		v, ok := m.Opts[k]
		if !ok {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			b.errf(pos(k), "invalid_value", "%s must be an integer between %d and %d", k, lo, hi)
			return
		}
		*dst = n
	}
	enum := func(k string, dst *string, allowed ...string) {
		v, ok := m.Opts[k]
		if !ok {
			return
		}
		v = strings.ToLower(v)
		if !contains(allowed, v) {
			b.errf(pos(k), "invalid_value", "%s must be one of %s", k, strings.Join(allowed, ", "))
			return
		}
		*dst = v
	}
	if v, ok := m.Opts["title"]; ok {
		if v == "" || len([]rune(v)) > 64 {
			b.errf(pos("title"), "invalid_value", "title must be 1 to 64 characters")
		} else {
			m.Title = v
		}
	}
	m.Description, m.Group = m.Opts["description"], m.Opts["group"]
	if v, ok := m.Opts["tags"]; ok {
		tags, err := parseTags(v)
		if err != nil {
			b.errf(pos("tags"), "invalid_value", "%v", err)
		}
		m.Tags = tags
	}
	dur("retry", &m.Retry, false)
	dur("timeout", &m.Timeout, false)
	num("attempts", &m.Attempts, 1, 100)
	num("recover-attempts", &m.RecoverAttempts, 1, 100)
	enum("on-insufficient", &m.OnInsufficient, "unknown", "critical")
	enum("on-filtered", &m.OnFiltered, "fail", "abstain")
	enum("on-node-loss", &m.OnNodeLoss, "degrade", "unknown")
	enum("scope", &m.Scope, "auto", "lan", "wan")
	dur("partial-escalate", &m.PartialEscalate, true)
	dur("degraded", &m.Degraded, false)
	num("cert-days", &m.CertDays, 0, 3650)
	num("cert-critical-days", &m.CertCritical, 0, 3650)
	if v, ok := m.Opts["quorum"]; ok {
		if err := ValidateQuorum(v); err != nil {
			b.errf(pos("quorum"), "invalid_value", "%v", err)
		}
		m.Quorum = v
	}
	if v, ok := m.Opts["policy"]; ok {
		m.Policy = v
	} else if scope != nil && scope.policy != "" {
		m.Policy = scope.policy
	}
	if scope != nil {
		m.Area = scope.spec
	}
	region, hasRegion := m.Opts["region"]
	if v, ok := m.Opts["nodes"]; ok {
		if hasRegion {
			b.errf(pos("region"), "invalid_value", "%s: use nodes= or region=, not both", m.Name)
		}
		var entries []string
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				entries = append(entries, n)
			}
		}
		m.Nodes = b.expandNodes(entries, pos("nodes"))
		if len(entries) == 1 && strings.HasPrefix(entries[0], "region:") {
			m.Region = strings.TrimPrefix(entries[0], "region:")
		}
	} else if hasRegion {
		// region=NAME: the nodes of that region check it; region=global:
		// every node, whatever $SCOPE it is written under.
		switch region = strings.TrimSpace(region); {
		case region == "" || region == RegionGlobal:
			m.Region = RegionGlobal
		case !ValidName(region):
			b.errf(pos("region"), "invalid_value", "region must be a name or global")
		default:
			m.Region = region
			m.Nodes = b.expandNodes([]string{"region:" + region}, pos("region"))
		}
	} else if scope != nil {
		m.Nodes = b.expandNodes(scope.nodes, scope.pos)
		if r, ok := strings.CutPrefix(scope.spec, "region:"); ok {
			m.Region = r
		}
	}
	if m.Region == "" && len(m.Nodes) == 0 {
		m.Region = RegionGlobal
	}
	for _, k := range []string{"authorized", "disabled", "share", "mute", "system-channel"} {
		if v, ok := m.Opts[k]; ok {
			x, err := parseBoolOpt(v)
			if err != nil {
				b.errf(pos(k), "invalid_value", "%s: %v", k, err)
			}
			switch k {
			case "authorized":
				m.Authorized = x
			case "disabled":
				m.Disabled = x
			case "mute":
				m.Mute = x
			case "system-channel":
				m.SystemChannel = x
			default:
				m.Share = x
			}
		}
	}
	if v, ok := m.Opts["device"]; ok {
		m.Device = v
	}
	// team=admin is the built-in team, the same as no team=.
	if m.Team = m.Opts["team"]; m.Team == AdminTeam {
		m.Team = ""
	}
	if v, ok := m.Opts["parent"]; ok {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				m.Parents = append(m.Parents, p)
			}
		}
		m.parentPos = pos("parent")
	}
	if m.CertDays > 0 && m.CertCritical > 0 && m.CertCritical >= m.CertDays {
		b.warnf(pos("cert-critical-days"), "invalid_value", "cert-critical-days should be lower than cert-days")
	}

	b.target(m, r, explicitInterval)

	if m.Timeout >= m.Interval && m.Type != "PUSH" {
		b.warnf(r.NamePos, "timeout_ge_interval", "timeout %s is not shorter than the interval %s", m.Timeout, m.Interval)
	}
	m.Refs = b.refs(r.NamePos, m.Opts)
	if m.Type != "PUSH" {
		m.Refs = append(m.Refs, b.refs(r.NamePos, map[string]string{"target": m.Target})...)
	}
	b.plaintext(r.NamePos, "", m.Opts)
	if b.opt.ValidateMonitor != nil && m.Type != "PUSH" {
		if err := b.opt.ValidateMonitor(m); err != nil {
			b.errf(r.NamePos, "invalid_option", "%s: %v", m.Name, err)
		}
	}
	if m.Type == "HTTP" && m.Opts["basic"] != "" && m.Opts["bearer"] != "" {
		b.warnf(r.NamePos, "conflicting_auth", "%s sets both basic= and bearer=: a request carries one Authorization header, and bearer= is the one sent", m.Name)
	}
	m.MetaHash, m.ParamHash, m.DefHash = hashes(m)
	b.cfg.Monitors = append(b.cfg.Monitors, m)
}

// target parses and normalises the monitor target.
func (b *builder) target(m *Monitor, r Record, explicitInterval bool) {
	p := r.NamePos
	if r.TargetOK {
		p = r.Stmt.Tokens[0].Pos
		for _, t := range r.Stmt.Tokens {
			if t.Text == r.Target {
				p = t.Pos
			}
		}
	}
	switch m.Type {
	case "PUSH":
		if len(m.Nodes) == 0 {
			m.Nodes = []string{b.cfg.MName}
		}
		if len(m.Nodes) != 1 {
			b.errf(r.NamePos, "invalid_option", "PUSH requires exactly one receiving site")
		}
		if r.TargetOK && r.Target != "-" {
			b.errf(p, "syntax", "PUSH takes no target")
		}
		m.Period = m.Interval
		if v, ok := m.Opts["period"]; ok {
			d, err := tunables.ParseDuration(v)
			if err != nil || d <= 0 {
				b.errf(r.NamePos, "invalid_duration", "period: invalid duration %q", v)
			} else {
				m.Period = d
			}
		}
		m.Grace = b.cfg.Tunables.Duration("probe.push-grace")
		if v, ok := m.Opts["grace"]; ok {
			d, err := tunables.ParseDuration(v)
			if err != nil || d < 0 {
				b.errf(r.NamePos, "invalid_duration", "grace: invalid duration %q", v)
			} else {
				m.Grace = d
			}
		}
		m.Interval = m.Period
		return
	}
	if !r.TargetOK || r.Target == "" {
		b.errf(r.TypePos, "missing_target", "%s needs a target", m.Type)
		return
	}
	m.Target = r.Target
	switch m.Type {
	case "HTTP":
		u, err := url.Parse(r.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			b.errf(p, "invalid_target", "HTTP target must be an http:// or https:// URL")
		}
	case "TCP":
		host, port, err := net.SplitHostPort(r.Target)
		if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
			b.errf(p, "invalid_target", "TCP target must be host:port")
		}
	case "PING":
		if strings.Contains(strings.Trim(r.Target, "[]"), "/") || (strings.Count(r.Target, ":") == 1) {
			b.errf(p, "invalid_target", "PING target is a host name or address, without a port")
		}
	case "REDIS", "POSTGRES", "MYSQL", "GRPC":
		// Service checks are TCP checks that speak the service's protocol.
		host, port := strings.Trim(r.Target, "[]"), protoPorts[m.Type]
		if h, pt, err := net.SplitHostPort(r.Target); err == nil {
			host, port = h, pt
		}
		n, err := strconv.Atoi(port)
		if host == "" || strings.ContainsAny(host, "/@") || err != nil || n < 1 || n > 65535 {
			b.errf(p, "invalid_target", "%s target must be host or host:port", m.Type)
			return
		}
		m.Opts["proto"] = strings.ToLower(m.Type)
		m.Type, m.Target = "TCP", net.JoinHostPort(host, port)
	case "DOMAIN":
		// A registry lookup: hours apart is plenty, and registries limit
		// how often they may be asked.
		// (The target itself is checked with the other check options.)
		if !explicitInterval {
			m.Interval = 6 * time.Hour
		} else if m.Interval < time.Hour {
			b.errf(r.NamePos, "invalid_value", "DOMAIN checks at most hourly (interval %s)", m.Interval)
		}
		if _, ok := m.Opts["retry"]; !ok {
			m.Retry = 10 * time.Minute
		}
		if _, ok := m.Opts["timeout"]; !ok {
			m.Timeout = 20 * time.Second
		}
	case "DOCKER":
		// A container on each probing node: local, never a path to the internet.
		if _, ok := m.Opts["scope"]; !ok {
			m.Scope = "lan"
		}
	case "DNS", "NTP", "SNMP", "SYSLOG":
		kw := strings.ToLower(m.Type)
		host, port := r.Target, ""
		if h, pt, err := net.SplitHostPort(r.Target); err == nil {
			host, port = h, pt
		}
		word := kw
		if port != "" {
			word = port
		}
		m.Type = "UDP"
		b.udp(m, p, host, word, kw, explicitInterval)
		if port != "" {
			if _, ok := m.Opts["template"]; !ok && m.UDP != nil {
				m.UDP.Template, m.UDP.Keyword = kw, kw
				if e, ok := keywords.Lookup(kw); ok {
					m.UDP.Entry, m.UDP.Depth = e, e.Depth
				}
			}
		}
		m.Target = net.JoinHostPort(host, strconv.Itoa(portOr(m.UDP)))
	case "UDP":
		host, word, err := net.SplitHostPort(r.Target)
		if err != nil || host == "" {
			b.errf(p, "invalid_target", "UDP target must be host:keyword or host:port")
			return
		}
		b.udp(m, p, host, word, "", explicitInterval)
	}
}

// protoPorts are the default ports of the service checks.
var protoPorts = map[string]string{"REDIS": "6379", "POSTGRES": "5432", "MYSQL": "3306", "GRPC": "50051"}

func portOr(u *UDPTarget) int {
	if u == nil {
		return 0
	}
	return u.Port
}

func (b *builder) udp(m *Monitor, p Pos, host, word, shorthand string, explicitInterval bool) {
	res, err := keywords.Resolve(word, b.cfg.Dialect)
	if err != nil {
		b.errf(p, "invalid_keyword", "%v", err)
		return
	}
	u := &UDPTarget{Host: host, Port: res.Port, Template: "raw", Depth: keywords.L1}
	if res.Hint != "" && shorthand == "" {
		b.warnf(p, "keyword_hint", "%s", res.Hint)
	}
	if e := res.Entry; e != nil {
		u.Entry, u.Keyword, u.Depth = e, e.Keyword, e.Depth
		if e.Template != "" {
			u.Template = e.Template
		}
		if e.Depth == keywords.L0 {
			b.warnf(p, "keyword_l0", "%s cannot be checked over UDP: %s", e.Keyword, e.Suggest)
		}
		if e.Reflective && !m.Authorized {
			b.warnf(p, "reflective_unauthorized", "%s replies can be abused for reflection; add authorized=yes if this is your own device", e.Keyword)
		}
		if e.DefaultIntervalSec > 0 && !explicitInterval {
			m.Interval = time.Duration(e.DefaultIntervalSec) * time.Second
		}
		if e.Stateful {
			min := b.cfg.Tunables.Duration("probe.stateful-min-interval")
			if m.Interval < min {
				b.warnf(p, "stateful_interval", "%s creates state on the target: interval raised to %s", e.Keyword, min)
				m.Interval = min
			}
			if m.Retry < min {
				m.Retry = min
			}
		}
	}
	if t, ok := m.Opts["template"]; ok {
		u.Template = strings.ToLower(t)
	}
	m.UDP = u
}

func (b *builder) node(r Record) {
	n := &Node{Name: r.Name, Probe: true, LocalDetect: true, Policy: "system", Opts: map[string]string{}, Pos: r.NamePos, Lines: lines(r.Stmt)}
	n.NodeTimeout = b.cfg.Tunables.Duration("cluster.node-timeout")
	if r.TargetOK {
		n.Description = r.Target
	}
	for _, o := range r.Opts {
		n.Opts[o.Key] = o.Value
		switch o.Key {
		case "probe", "local-detect":
			v, err := parseBoolOpt(o.Value)
			if err != nil {
				b.errf(o.Pos, "invalid_value", "%s: %v", o.Key, err)
			}
			if o.Key == "probe" {
				n.Probe = v
			} else {
				n.LocalDetect = v
			}
		case "anchors":
			if strings.EqualFold(o.Value, "none") {
				n.NoAnchors = true
				continue
			}
			for _, grp := range strings.Split(o.Value, ";") {
				kind, list, ok := strings.Cut(grp, ":")
				if !ok || (kind != "lan" && kind != "wan") {
					// "a,b" without a prefix means lan; IPv6 addresses
					// contain ':' so only lan:/wan: prefixes count.
					kind, list = "lan", grp
				}
				for _, a := range strings.Split(list, ",") {
					a = strings.TrimSpace(a)
					if a == "" {
						continue
					}
					if net.ParseIP(strings.Trim(a, "[]")) == nil {
						b.errf(o.Pos, "invalid_value", "anchor %q must be an IP address", a)
						continue
					}
					if kind == "lan" {
						n.AnchorsLAN = append(n.AnchorsLAN, a)
					} else {
						n.AnchorsWAN = append(n.AnchorsWAN, a)
					}
				}
			}
		case "region":
			n.Region = o.Value
		case "policy":
			n.Policy = o.Value
		case "standby":
			u, err := url.Parse(o.Value)
			if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
				b.errf(o.Pos, "invalid_value", "standby must be the https://host:port this node serves on")
			}
			n.Standby = strings.TrimRight(o.Value, "/")
		case "node-timeout":
			d, err := tunables.ParseDuration(o.Value)
			if err != nil || d <= 0 {
				b.errf(o.Pos, "invalid_duration", "node-timeout: invalid duration")
			}
			n.NodeTimeout = d
		default:
			b.errf(o.Pos, "unknown_option", "unknown NODE option %s", o.Key)
		}
	}
	b.cfg.Nodes = append(b.cfg.Nodes, n)
}

func (b *builder) channel(r Record) {
	c := &Channel{Name: r.Name, Opts: map[string]string{}, Pos: r.NamePos, Lines: lines(r.Stmt)}
	if !r.TargetOK || r.Target == "" {
		b.errf(r.TypePos, "missing_target", "CHANNEL needs a kind (webhook, feishu, telegram, email, ...)")
		return
	}
	c.Kind = strings.ToLower(r.Target)
	for _, o := range r.Opts {
		switch o.Key {
		case "period":
			c.Period = o.Value
		case "members":
			if c.Kind != "group" {
				b.errf(o.Pos, "unknown_option", "members applies to group channels")
			}
			for _, x := range strings.Split(o.Value, ",") {
				if x = strings.TrimSpace(x); x != "" {
					c.Members = append(c.Members, x)
				}
			}
		case "retries":
			n, err := strconv.Atoi(o.Value)
			if err != nil || n < 0 || n > 5 {
				b.errf(o.Pos, "invalid_value", "retries must be 0 to 5")
			}
			c.Retries = n
		case "rate":
			if _, err := tunables.ParseRate(o.Value); err != nil {
				b.errf(o.Pos, "invalid_value", "rate: %v", err)
			}
			c.Rate = o.Value
		case "system":
			v, err := parseBoolOpt(o.Value)
			if err != nil {
				b.errf(o.Pos, "invalid_value", "system: %v", err)
			}
			c.System = v
		case "report":
			v, err := parseBoolOpt(o.Value)
			if err != nil {
				b.errf(o.Pos, "invalid_value", "report: %v", err)
			}
			c.NoReport = !v
		default:
			c.Opts[o.Key] = o.Value
		}
	}
	c.Refs = b.refs(r.NamePos, c.Opts)
	b.plaintext(r.NamePos, c.Kind, c.Opts)
	if c.Kind == "group" {
		// A contact group: the members are checked once all channels exist.
		if len(c.Members) == 0 {
			b.errf(r.NamePos, "missing_option", "group channel %s needs members=channel[,channel...]", c.Name)
		}
		rotation := false
		for k := range c.Opts {
			if legacyRotationOpts[k] {
				rotation = true
			} else {
				b.errf(r.NamePos, "unknown_option", "group channel %s: option %s does not apply", c.Name, k)
			}
		}
		// On-call rotations were removed. A file that still has one keeps
		// loading, and its group tells every member, so nobody who used to
		// be reached is left out.
		if rotation {
			b.warnf(r.NamePos, "rotation_removed", "group channel %s: on-call rotations were removed; rotate=, handoff=, start= and tz= are ignored and every member is notified", c.Name)
		}
		b.cfg.Channels = append(b.cfg.Channels, c)
		return
	}
	if b.opt.ValidateChannel != nil {
		if err := b.opt.ValidateChannel(c.Kind, c.Opts); err != nil {
			b.errf(r.NamePos, "invalid_channel", "%s: %v", c.Name, err)
		}
	}
	b.cfg.Channels = append(b.cfg.Channels, c)
}

// period builds a PERIOD record: name IN PERIOD "mon-fri 09:00-18:00; ...".
func (b *builder) period(r Record) {
	if !r.TargetOK || r.Target == "" {
		b.errf(r.TypePos, "missing_target", `PERIOD needs its rules, e.g. "mon-fri 09:00-18:00; sat 10:00-14:00"`)
		return
	}
	sp, err := schedule.ParsePeriod(r.Target)
	if err != nil {
		b.errf(r.TypePos, "invalid_value", "%v", err)
		return
	}
	for _, o := range r.Opts {
		b.errf(o.Pos, "unknown_option", "unknown PERIOD option %s", o.Key)
	}
	b.cfg.Periods = append(b.cfg.Periods, &Period{Name: r.Name, Spec: sp, Pos: r.NamePos, Lines: lines(r.Stmt)})
}

func (b *builder) newPolicy(name string) *Policy {
	ts := b.cfg.Tunables
	return &Policy{
		Name: name, Mode: "fallback", Repeat: ts.Duration("notify.repeat"), Max: ts.Int("notify.max"),
		Delay: ts.Duration("notify.delay"), ReopenWindow: ts.Duration("notify.reopen-window"),
		Recovery: true, AckClearOnChange: true, RecoveryAfterAck: true, Opts: map[string]string{},
	}
}

func (b *builder) policyRec(r Record) {
	if r.TargetOK && r.Target != "-" {
		b.errf(r.TypePos, "syntax", "POLICY takes no target")
	}
	p := b.newPolicy(r.Name)
	p.Pos, p.Lines = r.NamePos, lines(r.Stmt)
	for _, o := range r.Opts {
		p.Opts[o.Key] = o.Value
		dur := func(dst *time.Duration) {
			d, err := tunables.ParseDuration(o.Value)
			if err != nil || d < 0 {
				b.errf(o.Pos, "invalid_duration", "%s: invalid duration %q", o.Key, o.Value)
				return
			}
			*dst = d
		}
		flag := func(dst *bool) {
			v, err := parseBoolOpt(o.Value)
			if err != nil {
				b.errf(o.Pos, "invalid_value", "%s: %v", o.Key, err)
			}
			*dst = v
		}
		switch o.Key {
		case "chain":
			for _, c := range strings.Split(o.Value, ",") {
				if c = strings.TrimSpace(c); c != "" {
					p.Chain = append(p.Chain, c)
				}
			}
		case "network":
			for _, c := range strings.Split(o.Value, ",") {
				if c = strings.TrimSpace(c); c != "" {
					p.Network = append(p.Network, c)
				}
			}
		case "mode":
			v := strings.ToLower(o.Value)
			if v != "fallback" && v != "all" {
				b.errf(o.Pos, "invalid_value", "mode must be fallback or all")
			}
			p.Mode = v
		case "repeat":
			dur(&p.Repeat)
		case "delay":
			dur(&p.Delay)
		case "reopen-window":
			dur(&p.ReopenWindow)
		case "ack-expire":
			dur(&p.AckExpire)
		case "max":
			n, err := strconv.Atoi(o.Value)
			if err != nil || n < 0 || n > 1000 {
				b.errf(o.Pos, "invalid_value", "max must be 0 (unlimited) to 1000")
			}
			p.Max = n
			if n == 0 {
				b.warnf(o.Pos, "unlimited_notifications", "max=0 sends notifications without limit")
			}
		case "recovery":
			flag(&p.Recovery)
		case "notify-degraded":
			flag(&p.NotifyDegraded)
		case "ack-clear-on-change":
			flag(&p.AckClearOnChange)
		case "recovery-after-ack":
			flag(&p.RecoveryAfterAck)
		case "escalate":
			// escalate=30m:leader,2h:director+sms — after each delay, while
			// the incident is open and unacknowledged.
			var last time.Duration
			for _, step := range strings.Split(o.Value, ",") {
				after, chans, ok := strings.Cut(strings.TrimSpace(step), ":")
				d, err := tunables.ParseDuration(after)
				if !ok || err != nil || d <= 0 || chans == "" {
					b.errf(o.Pos, "invalid_value", "escalate: %q must be DELAY:channel[+channel]", step)
					continue
				}
				if d <= last {
					b.errf(o.Pos, "invalid_value", "escalate: delays must increase")
				}
				last = d
				p.Escalate = append(p.Escalate, Escalation{After: d, Channels: strings.Split(chans, "+")})
			}
		case "last-resort":
			b.errf(o.Pos, "not_allowed", "last-resort can only be set in the server configuration, not in monitors.conf")
		default:
			b.errf(o.Pos, "unknown_option", "unknown POLICY option %s", o.Key)
		}
	}
	if len(p.Chain) == 0 {
		b.errf(r.NamePos, "missing_option", "POLICY needs chain=channel[,channel...]")
	}
	b.cfg.Policies = append(b.cfg.Policies, p)
}

func (b *builder) statusPage(r Record) {
	sp := &StatusPage{Name: r.Name, Title: r.Name, Path: "/status/" + r.Name, Windows: []string{"24h", "7d", "30d", "90d"}, Pos: r.NamePos, Lines: lines(r.Stmt)}
	if r.TargetOK {
		sp.Title = r.Target
	}
	for _, o := range r.Opts {
		switch o.Key {
		case "select":
			// The same selector as MAINT: only fields the configuration
			// decides, so a page's content never changes with the outages
			// it reports.
			sp.Select = o.Value
			if _, err := ParseSelector(o.Value); err != nil {
				b.errf(o.Pos, "invalid_select", "status page select: %v", err)
			}
		case "windows":
			sp.Windows = strings.Split(o.Value, ",")
			for _, w := range sp.Windows {
				if _, err := tunables.ParseDuration(w); err != nil {
					b.errf(o.Pos, "invalid_value", "windows: invalid duration %q", w)
				}
			}
		case "path":
			if !strings.HasPrefix(o.Value, "/") {
				b.errf(o.Pos, "invalid_value", "path must start with /")
			}
			sp.Path = o.Value
		case "logo":
			sp.Logo = o.Value
		case "footer":
			sp.Footer = o.Value
		case "theme":
			sp.Theme = o.Value
		case "domain":
			// Served at the root of its own host name (behind a proxy or
			// with a certificate for it).
			d := strings.ToLower(strings.TrimSuffix(o.Value, "."))
			if !hostnameRe.MatchString(d) || !strings.Contains(d, ".") {
				b.errf(o.Pos, "invalid_value", "domain must be a host name such as status.example.com")
			}
			sp.Domain = d
		case "subscribe":
			// E-mail subscriptions, sent through this e-mail channel.
			sp.Subscribe, sp.subscribePos = o.Value, o.Pos
		default:
			b.errf(o.Pos, "unknown_option", "unknown STATUSPAGE option %s", o.Key)
		}
	}
	b.cfg.StatusPages = append(b.cfg.StatusPages, sp)
}

// implicitPolicies adds "default" and "system" when they are not written:
// they may be referenced anyway. "system" inherits from "default".
func (b *builder) implicitPolicies() {
	def := b.cfg.FindPolicy("default")
	if def == nil {
		def = b.newPolicy("default")
		def.Implicit = true
		b.cfg.Policies = append(b.cfg.Policies, def)
	}
	if b.cfg.FindPolicy("system") == nil {
		sys := *def
		sys.Name, sys.Implicit, sys.Opts = "system", true, def.Opts
		// The system channels (system=yes), in file order, when there are.
		var chain []string
		for _, c := range b.cfg.Channels {
			if c.System {
				chain = append(chain, c.Name)
			}
		}
		if len(chain) > 0 {
			sys.Chain, sys.Mode, sys.Escalate, sys.Network = chain, "fallback", nil, nil
		}
		b.cfg.Policies = append(b.cfg.Policies, &sys)
	}
	if b.cfg.FindPolicy(MutePolicy) == nil {
		m := b.newPolicy(MutePolicy)
		m.Implicit, m.Mute = true, true
		b.cfg.Policies = append(b.cfg.Policies, m)
	}
	// Each team with channels gets a policy of them: its monitors are
	// notified through it.
	for _, t := range b.cfg.AllTeams() {
		if len(t.Channels) == 0 {
			continue
		}
		p := b.newPolicy(TeamPolicy(t.Name))
		p.Chain, p.Implicit, p.Team = t.Channels, true, t.Name
		if t.Mode == "all" {
			p.Mode = "all"
		}
		b.cfg.Policies = append(b.cfg.Policies, p)
	}
}

// MutePolicy is the policy of muted monitors (mute=yes): nothing is sent.
const MutePolicy = "mute"

// TeamPolicy names the policy made from a team's channels.
func TeamPolicy(team string) string { return "team:" + team }

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

func (b *builder) crossRefs() {
	domains := map[string]string{}
	for _, sp := range b.cfg.StatusPages {
		if sp.Domain != "" {
			if prev, dup := domains[sp.Domain]; dup {
				b.errf(sp.Pos, "invalid_value", "domain %s is already used by status page %s", sp.Domain, prev)
			}
			domains[sp.Domain] = sp.Name
		}
		if sp.Subscribe != "" {
			if c := b.cfg.FindChannel(sp.Subscribe); c == nil || c.Kind != "email" {
				b.errf(sp.subscribePos, "unknown_ref", "subscribe=%s must name an email channel", sp.Subscribe)
			}
		}
	}
	for _, p := range b.cfg.Policies {
		if p.Team != "" {
			continue // its channels are checked with the team
		}
		for _, c := range p.Chain {
			if b.cfg.FindChannel(c) == nil {
				b.errf(p.Pos, "unknown_channel", "policy %s: channel %q is not defined", p.Name, c)
			}
		}
		for _, c := range p.Network {
			if b.cfg.FindChannel(c) == nil {
				b.errf(p.Pos, "unknown_channel", "policy %s: network channel %q is not defined", p.Name, c)
			}
		}
		for _, e := range p.Escalate {
			for _, c := range e.Channels {
				if b.cfg.FindChannel(c) == nil {
					b.errf(p.Pos, "unknown_channel", "policy %s: escalation channel %q is not defined", p.Name, c)
				}
			}
		}
	}
	for _, c := range b.cfg.Channels {
		if c.Period != "" && b.cfg.FindPeriod(strings.TrimPrefix(c.Period, "!")) == nil {
			b.errf(c.Pos, "unknown_period", "channel %s: period %q is not defined", c.Name, strings.TrimPrefix(c.Period, "!"))
		}
		for _, mem := range c.Members {
			switch m := b.cfg.FindChannel(mem); {
			case m == nil:
				b.errf(c.Pos, "unknown_channel", "group %s: channel %q is not defined", c.Name, mem)
			case m.Kind == "group":
				b.errf(c.Pos, "invalid_value", "group %s: %s is a group itself; groups do not nest", c.Name, mem)
			}
		}
	}
	nodes := map[string]bool{b.cfg.MName: true}
	for _, n := range b.cfg.Nodes {
		nodes[n.Name] = true
	}
	for _, m := range b.cfg.Monitors {
		if b.cfg.FindPolicy(m.Policy) == nil {
			b.errf(m.Pos, "unknown_policy", "%s: policy %q is not defined", m.Name, m.Policy)
		}
		for _, n := range m.Nodes {
			if !nodes[n] {
				b.errf(m.Pos, "unknown_node", "%s: node %q is not defined", m.Name, n)
			}
		}
	}
	b.checkParents()
	b.checkSameChecks()
	b.checkTeams()
	for _, n := range b.cfg.Nodes {
		if b.cfg.FindPolicy(n.Policy) == nil {
			b.errf(n.Pos, "unknown_policy", "node %s: policy %q is not defined", n.Name, n.Policy)
		}
	}
}

// hashes computes the change-class hashes of a monitor.
func hashes(m *Monitor) (meta, param, def string) {
	hm, hp, hd := sha256.New(), sha256.New(), sha256.New()
	keys := make([]string, 0, len(m.Opts))
	for k := range m.Opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(hd, "type=%s\ntarget=%s\n", m.Type, m.Target)
	if m.UDP != nil {
		fmt.Fprintf(hd, "udp=%s:%d/%s\n", m.UDP.Host, m.UDP.Port, m.UDP.Template)
	}
	fmt.Fprintf(hp, "interval=%s\nretry=%s\ntimeout=%s\nattempts=%d\n", m.Interval, m.Retry, m.Timeout, m.Attempts)
	for _, k := range keys {
		line := k + "=" + m.Opts[k] + "\n"
		switch {
		case metaOpts[k]:
			hm.Write([]byte(line))
		case paramOpts[k]:
			hp.Write([]byte(line))
		default:
			hd.Write([]byte(line))
		}
	}
	sum := func(h interface{ Sum([]byte) []byte }) string { return hex.EncodeToString(h.Sum(nil))[:16] }
	return sum(hm), sum(hp), sum(hd)
}

// ChangeClass orders how much a monitor changed.
type ChangeClass int

const (
	ChangeNone       ChangeClass = iota
	ChangeMeta                   // title, tags...: nothing to reset
	ChangeParams                 // timing, quorum, policy: keep state, reschedule
	ChangeNodes                  // participating nodes changed
	ChangeDefinition             // the check itself changed: start over as PENDING
)

// ClassifyChange compares two versions of a monitor.
func ClassifyChange(old, new *Monitor) ChangeClass {
	switch {
	case old.DefHash != new.DefHash:
		return ChangeDefinition
	case strings.Join(old.Nodes, ",") != strings.Join(new.Nodes, ","):
		return ChangeNodes
	case old.ParamHash != new.ParamHash:
		return ChangeParams
	case old.MetaHash != new.MetaHash:
		return ChangeMeta
	}
	return ChangeNone
}

// teamUserRe is what a member entry may look like: a user name without
// spaces or commas (commas separate the list).
var teamUserRe = regexp.MustCompile(`^[^\s,]{1,64}$`)

// team reads a TEAM record:
//
//	ops IN TEAM title="Operations" members=li,wang,zhang channels=pager,mail mode=fallback
//
// The role lists of files before 2.6 (managers=, operators=, viewers=) are
// read as members: the role now comes from the account.
func (b *builder) team(r Record) {
	if r.TargetOK && r.Target != "-" {
		b.errf(r.TypePos, "syntax", "TEAM takes no target")
	}
	t := &Team{Name: r.Name, Mode: "fallback", Pos: r.NamePos, Lines: lines(r.Stmt)}
	seen := map[string]bool{}
	for _, o := range r.Opts {
		switch o.Key {
		case "title":
			if o.Value == "" || len([]rune(o.Value)) > 64 {
				b.errf(o.Pos, "invalid_value", "title must be 1 to 64 characters")
			}
			t.Title = o.Value
		case "description":
			t.Description = o.Value
		case "policy":
			t.Policy = o.Value
		case "tenant":
			// Reserved for a later release; not used.
		case "channels":
			for _, c := range strings.Split(o.Value, ",") {
				if c = strings.TrimSpace(c); c != "" {
					t.Channels = append(t.Channels, c)
				}
			}
		case "mode":
			v := strings.ToLower(o.Value)
			if v != "fallback" && v != "all" {
				b.errf(o.Pos, "invalid_value", "mode must be fallback or all")
			}
			t.Mode = v
		case "members", "managers", "operators", "viewers":
			if o.Key != "members" {
				t.Legacy = true
			}
			for _, u := range strings.Split(o.Value, ",") {
				if u = strings.TrimSpace(u); u == "" {
					continue
				}
				if !teamUserRe.MatchString(u) {
					b.errf(o.Pos, "invalid_value", "%s: %q is not a user name", o.Key, u)
					continue
				}
				if seen[u] {
					b.errf(o.Pos, "invalid_value", "%s is listed twice in team %s", u, r.Name)
					continue
				}
				seen[u] = true
				t.Members = append(t.Members, u)
			}
		default:
			b.errf(o.Pos, "unknown_option", "TEAM has no option %s (title, description, members, channels, mode)", o.Key)
		}
	}
	if t.Title == "" {
		t.Title = t.Name
	}
	b.cfg.Teams = append(b.cfg.Teams, t)
}

// checkTeams verifies the references between teams, channels, policies
// and monitors.
func (b *builder) checkTeams() {
	for _, t := range b.cfg.Teams {
		if t.Policy != "" && b.cfg.FindPolicy(t.Policy) == nil {
			b.errf(t.Pos, "unknown_policy", "team %s: policy %q is not defined", t.Name, t.Policy)
		}
		for _, c := range t.Channels {
			if b.cfg.FindChannel(c) == nil {
				b.errf(t.Pos, "unknown_channel", "team %s: channel %q is not defined", t.Name, c)
			}
		}
	}
	for _, m := range b.cfg.Monitors {
		t := b.cfg.FindTeam(m.TeamOf())
		if t == nil {
			b.errf(m.Pos, "unknown_team", "%s: team %q is not defined (add: %s IN TEAM members=…)", m.Name, m.Team, m.Team)
		}
		// A team's monitors are notified through the team's channels. Files
		// from before 2.6 may still name a policy (policy= on the record or
		// through $DEFAULT, or on the team); it is kept until migrated.
		_, own := m.Opts["policy"]
		switch {
		case t == nil || own:
		case len(t.Channels) > 0:
			m.Policy = TeamPolicy(t.Name)
		case t.Policy != "":
			m.Policy = t.Policy
		}
		// The two exceptions a monitor may have.
		switch {
		case m.Mute:
			m.Policy = MutePolicy
		case m.SystemChannel:
			m.Policy = "system"
		}
		// A team with no channels at all is notified through the system
		// channels (and its administrators are told).
		if p := b.cfg.FindPolicy(m.Policy); p != nil && len(p.Chain) == 0 && !p.Mute && t != nil && len(t.Channels) == 0 {
			if sys := b.cfg.FindPolicy("system"); sys != nil && len(sys.Chain) > 0 {
				m.Policy, m.Unrouted = "system", true
			}
		}
	}
}

// CheckKey identifies what a monitor checks and from where: the type, the
// target (host names in lower case), the timeout, the options that change
// the check itself and the nodes. Two monitors with the same key send the
// same requests and judge the answers the same way; "" for push monitors.
func (m *Monitor) CheckKey() string {
	if m.Type == "PUSH" {
		return ""
	}
	target := m.Target
	switch {
	case m.UDP != nil:
		target = strings.ToLower(m.UDP.Host) + "|" + strconv.Itoa(m.UDP.Port) + "|" + m.UDP.Template
	case m.Type == "HTTP":
		if u, err := url.Parse(m.Target); err == nil {
			u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
			target = u.String()
		}
	case m.Type == "TCP":
		if h, p, err := net.SplitHostPort(m.Target); err == nil {
			target = strings.ToLower(h) + "|" + p
		}
	case m.Type == "PING":
		target = strings.ToLower(m.Target)
	}
	var opts []string
	for k, v := range m.Opts {
		if metaOpts[k] || paramOpts[k] || k == "authorized" {
			continue
		}
		opts = append(opts, k+"="+v)
	}
	sort.Strings(opts)
	nodes := append([]string(nil), m.Nodes...)
	sort.Strings(nodes)
	return strings.Join([]string{m.Type, target, m.Timeout.String(), strings.Join(opts, "\x00"), strings.Join(nodes, ",")}, "\x01")
}

// checkSameChecks points out monitors that check exactly what an earlier
// one checks: the target gets every request twice. share=yes on the later
// one uses one result for both; keeping both is fine when they are meant
// to differ only in thresholds or notifications.
func (b *builder) checkSameChecks() {
	first := map[string]*Monitor{}
	shared := map[string]bool{}
	for _, m := range b.cfg.Monitors {
		key := m.CheckKey()
		if key == "" || m.Disabled {
			continue
		}
		prev := first[key]
		if prev == nil {
			first[key] = m
			continue
		}
		shared[key] = true
		if !m.Share {
			b.warnf(m.Pos, "same_check", "%s checks exactly what %s (line %d) checks: add share=yes to use one check for both", m.Name, prev.Name, prev.Pos.Line)
		}
	}
	for _, m := range b.cfg.Monitors {
		if m.Share && !m.Disabled && !shared[m.CheckKey()] {
			b.warnf(m.Pos, "share_unused", "%s: share=yes has no effect, no other monitor checks exactly the same thing", m.Name)
		}
	}
}

// checkParents verifies parent= references: they must name other monitors
// and must not form a loop.
func (b *builder) checkParents() {
	for _, m := range b.cfg.Monitors {
		for _, p := range m.Parents {
			switch {
			case p == m.Name:
				b.errf(m.parentPos, "invalid_value", "%s cannot be its own parent", m.Name)
			case b.cfg.FindMonitor(p) == nil:
				b.errf(m.parentPos, "unknown_parent", "%s: parent %q is not a monitor", m.Name, p)
			}
		}
	}
	// Depth-first search for loops; report each loop once, at the monitor
	// where it was entered.
	const (
		unseen = iota
		active
		done
	)
	state := map[string]int{}
	var visit func(m *Monitor, path []string) bool
	visit = func(m *Monitor, path []string) bool {
		switch state[m.Name] {
		case active:
			b.errf(m.parentPos, "parent_loop", "parent loop: %s", strings.Join(append(path, m.Name), " → "))
			return true
		case done:
			return false
		}
		state[m.Name] = active
		for _, p := range m.Parents {
			if pm := b.cfg.FindMonitor(p); pm != nil && pm != m && visit(pm, append(path, m.Name)) {
				state[m.Name] = done
				return true
			}
		}
		state[m.Name] = done
		return false
	}
	for _, m := range b.cfg.Monitors {
		if state[m.Name] == unseen {
			visit(m, nil)
		}
	}
}

// maint builds a maintenance window:
//
//	name IN MAINT "title" select="tag:site:bj" every=sun start=02:00 duration=2h
//	name IN MAINT "title" select="group:core" at="2026-11-01 22:00" duration=3h
//	name IN MAINT "title" select="host:10.0.0.1" cron="30 2 * * 1-5" duration=30m
func (b *builder) maint(r Record) {
	mt := &Maint{Name: r.Name, Title: r.Name, Pos: r.NamePos, Lines: lines(r.Stmt)}
	if r.TargetOK {
		mt.Title = r.Target
	}
	loc := b.opt.Location
	if loc == nil {
		loc = time.Local
	}
	kinds := 0
	var every string
	startSet := false
	for _, o := range r.Opts {
		var err error
		switch o.Key {
		case "select":
			mt.Select, err = ParseSelector(o.Value)
		case "duration":
			mt.Schedule.Duration, err = tunables.ParseDuration(o.Value)
			if err == nil && (mt.Schedule.Duration <= 0 || mt.Schedule.Duration > schedule.MaxDuration) {
				err = fmt.Errorf("duration must be between 1m and %v", schedule.MaxDuration)
			}
		case "at":
			kinds++
			mt.Schedule.At, err = schedule.ParseAt(o.Value, loc)
		case "every":
			kinds++
			every = o.Value
			err = schedule.ParseEvery(o.Value, &mt.Schedule)
		case "start":
			startSet = true
			mt.Schedule.Start, err = schedule.ParseClock(o.Value)
		case "cron":
			kinds++
			mt.Schedule.Cron, err = schedule.ParseCron(o.Value)
		case "notify":
			mt.Notify, err = parseBoolOpt(o.Value)
		default:
			err = fmt.Errorf("unknown MAINT option %s", o.Key)
		}
		if err != nil {
			b.errf(o.Pos, "invalid_value", "%s: %v", o.Key, err)
		}
	}
	switch {
	case mt.Select.Empty():
		b.errf(r.NamePos, "missing_option", "MAINT %s needs select= (which monitors)", r.Name)
	case mt.Schedule.Duration == 0:
		b.errf(r.NamePos, "missing_option", "MAINT %s needs duration=", r.Name)
	case kinds != 1:
		b.errf(r.NamePos, "invalid_value", "MAINT %s needs exactly one of at=, every= or cron=", r.Name)
	case every != "" && !startSet:
		b.errf(r.NamePos, "missing_option", "MAINT %s: every= needs start=HH:MM", r.Name)
	}
	for _, m := range b.cfg.Monitors {
		if mt.Select.Match(m) {
			mt.Monitors = append(mt.Monitors, m.Name)
		}
	}
	if !mt.Select.Empty() && len(mt.Monitors) == 0 {
		b.warnf(r.NamePos, "empty_select", "MAINT %s selects no monitor", r.Name)
	}
	b.cfg.Maints = append(b.cfg.Maints, mt)
}
