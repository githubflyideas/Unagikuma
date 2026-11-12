package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/keywords"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// engineOpts are monitor options interpreted by the engine, not the probe.
var engineOpts = map[string]bool{
	"title": true, "description": true, "tags": true, "group": true, "timeout": true, "retry": true,
	"attempts": true, "recover-attempts": true, "nodes": true, "quorum": true, "on-insufficient": true,
	"on-node-loss": true, "partial-escalate": true, "scope": true, "policy": true, "degraded": true,
	"disabled": true, "period": true, "grace": true, "parent": true, "device": true, "handler": true, "was": true,
	"share": true, "team": true, "region": true, "mute": true, "system-channel": true, "tenant": true,
}

// ProbeSpec builds the probe spec of a monitor. resolve substitutes
// ${secret:} / ${env:} references (nil keeps them, for validation).
func ProbeSpec(m *config.Monitor, resolve func(string) (string, error)) (probe.Spec, error) {
	sub := func(s string) (string, error) {
		if resolve == nil {
			return s, nil
		}
		return resolve(s)
	}
	o := map[string]string{}
	for k, v := range m.Opts {
		if engineOpts[k] {
			continue
		}
		rv, err := sub(v)
		if err != nil {
			return probe.Spec{}, err
		}
		o[k] = rv
	}
	target, err := sub(m.Target)
	if err != nil {
		return probe.Spec{}, err
	}
	if m.UDP != nil {
		target = net.JoinHostPort(m.UDP.Host, strconv.Itoa(m.UDP.Port))
		if _, set := o["template"]; !set {
			o["template"] = m.UDP.Template
		}
	}
	spec := probe.Spec{Name: m.Name, Type: m.Type, Target: target, Opts: o, Timeout: m.Timeout, Share: m.Share}
	if resolve != nil {
		// The values ${secret:} references stand for, to scrub them from
		// what the probe records.
		seen := map[string]bool{}
		for _, v := range append([]string{m.Target}, mapValues(m.Opts)...) {
			for _, ref := range refRe.FindAllString(v, -1) {
				if strings.HasPrefix(ref, "${secret:") && !seen[ref] {
					seen[ref] = true
					if val, err := resolve(ref); err == nil && val != "" {
						spec.Secrets = append(spec.Secrets, val)
					}
				}
			}
		}
	}
	return spec, nil
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

var refRe = regexp.MustCompile(`\$\{(secret|env):([A-Za-z0-9_.-]+)\}`)

// resolver substitutes references with secret values and allowed
// environment variables.
func (a *App) resolver() func(string) (string, error) {
	return func(s string) (string, error) {
		var firstErr error
		out := refRe.ReplaceAllStringFunc(s, func(ref string) string {
			m := refRe.FindStringSubmatch(ref)
			switch m[1] {
			case "env":
				if !strings.HasPrefix(m[2], "UNAGIKUMA_") && !contains(a.opts.AllowEnv, m[2]) {
					firstErr = fmt.Errorf("${env:%s} is not allowed", m[2])
					return ""
				}
				return os.Getenv(m[2])
			default:
				v, err := a.secrets.Get(m[2])
				if err != nil && firstErr == nil {
					firstErr = fmt.Errorf("secret %s: %v", m[2], err)
				}
				return v
			}
		})
		return out, firstErr
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ApplyProbeDefaults gives a one-off probe (the probe command, "check
// now" before saving) the thresholds a running monitor gets from the
// tunables, so both judge the same way.
func ApplyProbeDefaults(spec *probe.Spec, ts tunables.Set) {
	certDefaults(spec, ts)
	pingDefaults(spec, ts)
}

// certDefaults gives TLS checks the default certificate expiry thresholds
// (probe.cert-warn-days, probe.cert-critical-days) unless the monitor sets
// its own; 0 written on the monitor turns a threshold off.
func certDefaults(spec *probe.Spec, ts tunables.Set) {
	on := func(v string) bool { v = strings.ToLower(v); return v == "yes" || v == "true" || v == "on" || v == "1" }
	tls := spec.Type == "HTTP" && strings.HasPrefix(strings.ToLower(spec.Target), "https://") || spec.Type == "TCP" && on(spec.Opts["tls"])
	if !tls {
		return
	}
	if spec.Opts == nil {
		spec.Opts = map[string]string{}
	}
	for key, tun := range map[string]string{"cert-days": "probe.cert-warn-days", "cert-critical-days": "probe.cert-critical-days"} {
		if _, set := spec.Opts[key]; !set {
			if n := ts.Int(tun); n > 0 {
				spec.Opts[key] = strconv.Itoa(n)
			}
		}
	}
}

// pingDefaults gives PING checks the default loss threshold
// (probe.ping-loss) unless the monitor sets loss=; 0 turns it off.
func pingDefaults(spec *probe.Spec, ts tunables.Set) {
	if spec.Type != "PING" {
		return
	}
	if _, set := spec.Opts["loss"]; set {
		return
	}
	if n := ts.Int("probe.ping-loss"); n > 0 {
		if spec.Opts == nil {
			spec.Opts = map[string]string{}
		}
		spec.Opts["loss"] = strconv.Itoa(n) + "%"
	}
}

// validators are the config.Options hooks.
func (a *App) validators() config.Options {
	return config.Options{
		AllowEnv:    a.opts.AllowEnv,
		Location:    a.opts.Location,
		SecretKnown: a.secrets.Has,
		ValidateMonitor: func(m *config.Monitor) error {
			s, err := ProbeSpec(m, nil)
			if err != nil {
				return err
			}
			// Unresolved references cannot be validated statically.
			for k, v := range s.Opts {
				if strings.Contains(v, "${") {
					delete(s.Opts, k)
				}
			}
			if strings.Contains(s.Target, "${") {
				return nil
			}
			if h := m.Opts["handler"]; h != "" {
				if err := probe.ValidateHandler(h); err != nil {
					return fmt.Errorf("handler: %v", err)
				}
			}
			return probe.Validate(s)
		},
		ValidateChannel: ValidateChannel,
		ChannelWarnings: notify.TemplateProblems,
	}
}

// engineConfig translates a validated configuration for the engine.
// Monitors whose secrets cannot be resolved still run: the probe abstains
// with secret_unavailable so the operator sees why.
func (a *App) engineConfig(cfg *config.Config) engine.Config {
	ec := engine.Config{
		Serial: cfg.Serial, Instance: a.instance(), Lang: a.messageLang(), BaseURL: a.baseURL(),
		Location: a.opts.Location, Tunables: cfg.Tunables, LocalDetect: true,
		Policies: map[string]engine.PolicySpec{}, Channels: map[string]engine.ChannelSpec{},
	}
	resolve := a.resolver()
	degradedMax := cfg.Tunables.Int("notify.degraded-max")
	selfProbes, probers := clusterNodes(cfg, &ec)
	ec.SourceNode = cfg.MName
	ec.ObservationSites = map[string][]string{}
	for _, m := range cfg.Monitors {
		sites := participants(m.Nodes, cfg.MName, selfProbes, probers)
		ms := monitorSpec(m, cfg, resolve, len(ec.Nodes) > 0, sites)
		if m.Type == "PUSH" {
			sites = append([]string(nil), m.Nodes...)
		}
		ec.ObservationSites[m.Name] = sites
		ec.Monitors = append(ec.Monitors, ms)
	}
	for _, mt := range cfg.Maints {
		ec.Maints = append(ec.Maints, engine.MaintSpec{Name: mt.Name, Title: mt.Title, Monitors: mt.Monitors, Schedule: mt.Schedule, Notify: mt.Notify})
	}
	ec.Maints = append(ec.Maints, a.changeWindows(cfg)...)
	leftOut := map[string][]directory.Issue{}
	for _, c := range cfg.Channels {
		ec.Channels[c.Name] = a.channelSpec(cfg, c, resolve, leftOut)
	}
	a.leftOut.Store(&leftOut)
	for _, p := range cfg.Policies {
		ec.Policies[p.Name] = policySpec(p, degradedMax)
	}
	a.addManagerNotices(cfg, &ec)
	a.addTeamFallbacks(cfg, &ec)
	ec.ReportTeams = reportTeams(cfg, ec.Policies)
	localAnchors(cfg, &ec)
	return ec
}

// monitorSpec translates one monitor. clustered says the file names remote
// nodes; sites are the nodes that take part in judging it.
func monitorSpec(m *config.Monitor, cfg *config.Config, resolve func(string) (string, error), clustered bool, sites []string) engine.MonitorSpec {
	spec, err := ProbeSpec(m, resolve)
	certDefaults(&spec, cfg.Tunables)
	pingDefaults(&spec, cfg.Tunables)
	depth := string(probe.DepthOf(spec))
	weak := depth == string(keywords.L1) || (m.UDP != nil && m.UDP.Entry != nil && m.UDP.Entry.Stateful)
	ms := engine.MonitorSpec{
		Name: m.Name, Title: m.Title, Group: m.Group, Probe: spec, Depth: depth, Policy: m.Policy,
		Interval: m.Interval, Retry: m.Retry, Attempts: m.Attempts, RecoverAttempts: m.RecoverAttempts,
		SlowAfter: m.Degraded, Paused: m.Disabled, DefHash: m.DefHash, Parents: m.Parents,
		Host: m.HostKey(), Address: m.Host(), Area: m.Area, Ping: m.Type == "PING",
		Quorum:  engine.QuorumRule{Spec: m.Quorum, OnNodeLoss: m.OnNodeLoss, OnInsufficient: m.OnInsufficient, Weak: weak},
		Handler: m.Opts["handler"], Team: m.Team,
	}
	if clustered && m.Type != "PUSH" {
		ms.Nodes = append([]string(nil), sites...)
		ms.PartialEscalate = m.PartialEscalate
		// A node with a wrong clock cannot judge a time server or how
		// close a certificate is to expiry.
		ms.TimeSensitive = spec.Opts["cert-days"] != "" && spec.Opts["cert-days"] != "0" ||
			spec.Opts["cert-critical-days"] != "" && spec.Opts["cert-critical-days"] != "0" || (m.UDP != nil && m.UDP.Keyword == "ntp")
	}
	if m.Scope == "lan" || m.Scope == "wan" {
		ms.Scope = netscope.Scope(m.Scope)
	}
	for _, t := range m.Tags {
		ms.Tags = append(ms.Tags, t.String())
	}
	if m.Type == "PUSH" {
		ms.Push, ms.PushPeriod, ms.PushGrace = true, m.Period, m.Grace
		ms.Nodes = append([]string(nil), m.Nodes...)
	}
	if err != nil {
		ms.SecretError = err.Error()
	}
	return ms
}

// channelSpec translates one channel, resolving secrets and recipients.
// Recipients left out are recorded in leftOut.
func (a *App) channelSpec(cfg *config.Config, c *config.Channel, resolve func(string) (string, error), leftOut map[string][]directory.Issue) engine.ChannelSpec {
	opts := map[string]string{}
	for k, v := range c.Opts {
		rv, err := resolve(v)
		if err != nil {
			a.log.Warn("channel reference", "channel", c.Name, "err", err)
		}
		opts[k] = rv
	}
	cs := engine.ChannelSpec{Channel: notify.Channel{Name: c.Name, Kind: c.Kind, Opts: opts}, Retries: c.Retries, RetriesSet: c.RetriesSet, Members: c.Members}
	if c.Rate != "" {
		cs.Rate, _ = tunables.ParseRate(c.Rate)
	}
	if key, kind, ok := directory.AddressKey(c.Kind); ok {
		// Recipients go through the contact directory: @handles become
		// addresses, people who left and dead addresses are dropped.
		send, issues := a.Directory().Resolve(kind, opts[key])
		opts[key] = strings.Join(send, ",")
		if keep := a.Directory().TakesReport(kind, send); len(keep) != len(send) {
			ro := maps.Clone(opts)
			ro[key] = strings.Join(keep, ",")
			cs.ReportOpts = ro
			cs.NoReport = len(keep) == 0
		}
		if len(send) == 0 {
			cs.NoRecipients = "nobody to send to: no recipient is an active contact (see Alerting › Contacts)"
		}
		for _, i := range issues {
			if i.Address != "" {
				leftOut[c.Name] = append(leftOut[c.Name], i)
			}
		}
	}
	if c.Period != "" {
		if p := cfg.FindPeriod(strings.TrimPrefix(c.Period, "!")); p != nil {
			cs.Period, cs.Outside = &p.Spec, strings.HasPrefix(c.Period, "!")
		}
	}
	// A daily report by SMS fills an approved alert template with report
	// text, and by voice it is a phone call every morning: these channels
	// take it only when asked to (report=yes).
	if c.NoReport || (!c.ReportSet && smsOrVoice(c.Kind)) {
		cs.NoReport = true
	}
	cs.Scope, cs.Family, cs.OnLink, cs.ByName = channelReach(c.Kind, opts)
	return cs
}

// smsOrVoice reports whether a channel kind sends text messages or calls.
func smsOrVoice(kind string) bool {
	return strings.HasSuffix(kind, "-sms") || strings.HasSuffix(kind, "-voice")
}

// policySpec translates one notification policy.
func policySpec(p *config.Policy, degradedMax int) engine.PolicySpec {
	ps := engine.PolicySpec{Name: p.Name, Chain: p.Chain, Mode: p.Mode, Notify: engine.NotifyPolicy{
		Max: p.Max, Repeat: p.Repeat, Delay: p.Delay, ReopenWindow: p.ReopenWindow, Recovery: p.Recovery,
		NotifyDegraded: p.NotifyDegraded, DegradedMax: degradedMax, AckExpire: p.AckExpire,
		AckClearOnChange: p.AckClearOnChange, RecoveryAfterAck: p.RecoveryAfterAck,
	}}
	ps.Network, ps.Mute = p.Network, p.Mute
	for _, x := range p.Escalate {
		ps.Escalate = append(ps.Escalate, engine.EscalationStep{After: x.After, Channels: x.Channels})
	}
	return ps
}

// localAnchors sets this server's own network anchors.
func localAnchors(cfg *config.Config, ec *engine.Config) {
	for _, n := range cfg.Nodes {
		if n.Name != cfg.MName {
			continue
		}
		ec.LocalDetect = n.LocalDetect && !n.NoAnchors
		for _, x := range n.AnchorsLAN {
			ec.Anchors = append(ec.Anchors, engine.AnchorSpec{Kind: engine.AnchorLAN, Addr: x})
		}
		for _, x := range n.AnchorsWAN {
			ec.Anchors = append(ec.Anchors, engine.AnchorSpec{Kind: engine.AnchorWAN, Addr: x})
		}
	}
}

// clusterNodes fills the engine's node list with the remote nodes and
// reports whether this server probes and which remote nodes probe. A file
// without remote nodes leaves ec.Nodes empty: single-machine mode.
func clusterNodes(cfg *config.Config, ec *engine.Config) (bool, []string) {
	selfProbes := true
	var probers []string
	for _, n := range cfg.Nodes {
		if n.Name == cfg.MName {
			selfProbes = n.Probe
			continue
		}
		ec.Nodes = append(ec.Nodes, engine.NodeSpec{
			Name: n.Name, Title: n.Description, Region: n.Region, Probe: n.Probe, Policy: n.Policy, Timeout: n.NodeTimeout,
		})
		if n.Probe {
			probers = append(probers, n.Name)
		}
	}
	if len(ec.Nodes) > 0 {
		ec.Self = cfg.MName
	}
	return selfProbes, probers
}

// participants lists the nodes that judge a monitor: the ones it names,
// otherwise every probing node.
func participants(named []string, self string, selfProbes bool, probers []string) []string {
	if len(named) > 0 {
		return append([]string(nil), named...)
	}
	var out []string
	if selfProbes {
		out = append(out, self)
	}
	return append(out, probers...)
}

// agentConfigs builds what each remote node runs: only the monitors it
// takes part in, with references already resolved, and its own anchors.
func agentConfigs(cfg *config.Config, ec engine.Config) map[string]*cluster.AgentConfig {
	out := map[string]*cluster.AgentConfig{}
	for _, n := range cfg.Nodes {
		if n.Name == cfg.MName {
			continue
		}
		ac := &cluster.AgentConfig{Serial: cfg.Serial, LocalDetect: n.LocalDetect && !n.NoAnchors, Tunables: cfg.Tunables.Overrides()}
		for _, x := range n.AnchorsLAN {
			ac.Anchors = append(ac.Anchors, engine.AnchorSpec{Kind: engine.AnchorLAN, Addr: x})
		}
		for _, x := range n.AnchorsWAN {
			ac.Anchors = append(ac.Anchors, engine.AnchorSpec{Kind: engine.AnchorWAN, Addr: x})
		}
		for _, m := range ec.Monitors {
			if !m.Paused && slices.Contains(m.Nodes, n.Name) {
				ac.Monitors = append(ac.Monitors, cluster.AgentMonitor{Name: m.Name, Probe: m.Probe, Scope: m.Scope, DefHash: m.DefHash, SecretError: m.SecretError})
			}
		}
		// Where the master can be found after a handover: every standby but
		// the node itself.
		ac.Masters, ac.Standby = []string{}, n.Standby != ""
		for _, o := range cfg.Nodes {
			if o.Standby != "" && o.Name != n.Name {
				ac.Masters = append(ac.Masters, o.Standby)
			}
		}
		b, _ := json.Marshal(ac)
		h := sha256.Sum256(b)
		ac.Hash = hex.EncodeToString(h[:8])
		out[n.Name] = ac
	}
	return out
}

// channelReach classifies where a channel's endpoint lives, so it can be
// paused (not failed) while that path is down.
func channelReach(kind string, opts map[string]string) (netscope.Scope, engine.Family, bool, bool) {
	host := ""
	switch kind {
	case "email":
		host = opts["host"]
	case "log":
		return netscope.LAN, engine.FamilyV4, true, false
	case "telegram":
		host = "api.telegram.org"
		if api := opts["api"]; api != "" {
			if u, err := url.Parse(api); err == nil {
				host = u.Hostname()
			}
		}
	default:
		for _, k := range []string{"url", "server", "homeserver"} {
			if u, err := url.Parse(opts[k]); err == nil && u.Hostname() != "" {
				host = u.Hostname()
				break
			}
		}
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return netscope.OfAddr(a), engine.FamilyOf(a), false, false
	}
	if s, known := netscope.OfHost(host); known {
		return s, engine.FamilyV4, false, true
	}
	return netscope.WAN, engine.FamilyV4, false, true
}

func (a *App) instance() string {
	if v, _ := a.store.Setting("instance_name"); v != "" {
		return v
	}
	if a.opts.Instance != "" {
		return a.opts.Instance
	}
	h, _ := os.Hostname()
	return h
}

// MessageLang is the language notifications are written in.
func (a *App) MessageLang() string { return a.messageLang() }

func (a *App) messageLang() string {
	if v, _ := a.store.Setting("message_lang"); v != "" {
		return v
	}
	return "en"
}

func (a *App) baseURL() string {
	if v, _ := a.store.Setting("base_url"); v != "" {
		return v
	}
	return a.opts.BaseURL
}

// Resolver substitutes ${secret:} and ${env:} references.
func (a *App) Resolver() func(string) (string, error) { return a.resolver() }

// BaseURL is the address links in messages point to.
func (a *App) BaseURL() string { return a.baseURL() }

// ListenAddr is the configured UI/agent listen address (e.g. :8077).
func (a *App) ListenAddr() string { return a.opts.Listen }

// SendDirect sends one message outside any notification policy (status
// page subscriptions).
func (a *App) SendDirect(ctx context.Context, ch notify.Channel, m *notify.Message) notify.Attempt {
	return a.sender.Send(ctx, ch, m)
}

// ChannelWarnings reports what is wrong with a channel's message templates
// (for the check and import commands, as the server reports it).
func ChannelWarnings(kind string, opts map[string]string) []string {
	return notify.TemplateProblems(kind, opts)
}

// ValidateChannel checks a channel's options as the server does when it
// loads the configuration (for the command-line check and import).
func ValidateChannel(kind string, opts map[string]string) error {
	if kind == "webhook" {
		if b := opts["body"]; b != "" {
			if err := notify.ValidateTemplate(b); err != nil {
				return err
			}
		}
	}
	return notify.ValidateChannel(kind, opts)
}

// reportTeams are the teams receiving the daily status report
// (notify.daily-report-teams), each through the policy its alerts go
// through: the team's channels, else the system channels. The admin
// team's report on every monitor keeps notify.daily-report-policy.
func reportTeams(cfg *config.Config, pols map[string]engine.PolicySpec) []engine.ReportTeam {
	var out []engine.ReportTeam
	seen := map[string]bool{}
	for _, name := range strings.Split(cfg.Tunables.Raw("notify.daily-report-teams"), ",") {
		name = strings.TrimSpace(name)
		t := cfg.FindTeam(name)
		if t == nil || seen[name] {
			continue
		}
		seen[name] = true
		pol := "system"
		if p := strings.TrimSpace(cfg.Tunables.Raw("notify.daily-report-policy")); p != "" && name == config.AdminTeam {
			if _, ok := pols[p]; ok {
				pol = p
			}
		}
		if pol == "system" {
			if _, ok := pols[config.TeamPolicy(name)]; ok {
				pol = config.TeamPolicy(name)
			} else if _, ok := pols[t.Policy]; ok && t.Policy != "" {
				pol = t.Policy
			}
		}
		out = append(out, engine.ReportTeam{Name: name, Policy: pol})
	}
	return out
}
