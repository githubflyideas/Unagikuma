package config

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/githubflyideas/unagikuma/internal/keywords"
	"github.com/githubflyideas/unagikuma/internal/schedule"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Severity of a diagnostic.
const (
	SevError   = "error"
	SevWarning = "warning"
)

// Diagnostic is a configuration error or warning at a position.
type Diagnostic struct {
	Pos      Pos    `json:"pos"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Msg      string `json:"message"`
}

func (d Diagnostic) Error() string { return d.Pos.String() + ": " + d.Severity + ": " + d.Msg }

// HasErrors reports whether any diagnostic is an error.
func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == SevError {
			return true
		}
	}
	return false
}

// Defaults are the SOA timing defaults.
type Defaults struct {
	Interval time.Duration
	Retry    time.Duration
	Timeout  time.Duration
	Attempts int
}

// Tag is a key[:value] label.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

func (t Tag) String() string {
	if t.Value == "" {
		return t.Key
	}
	return t.Key + ":" + t.Value
}

// TagMeta is the $TAG presentation of a tag key (or key:value).
type TagMeta struct {
	Color       string
	Description string
}

// Config is the validated semantic model of a file.
type Config struct {
	Serial   uint32
	MName    string // master node name (without trailing dot)
	RName    string // administrator e-mail (ops@example.com)
	Defaults Defaults
	Quorum   string
	Dialect  keywords.Dialect
	Tunables tunables.Set
	Tags     map[string]TagMeta

	Monitors    []*Monitor
	Nodes       []*Node
	Channels    []*Channel
	Policies    []*Policy
	StatusPages []*StatusPage
	Maints      []*Maint
	Periods     []*Period
}

// Period is a weekly time period channels are limited to.
type Period struct {
	Name  string
	Spec  schedule.Period
	Pos   Pos
	Lines [2]int
}

// Escalation notifies more channels while an incident stays unacknowledged.
type Escalation struct {
	After    time.Duration
	Channels []string
}

// Maint is a maintenance window: while it is open the selected monitors
// are not notified and their failures do not count against uptime.
type Maint struct {
	Name     string
	Title    string
	Select   *Selector
	Schedule schedule.Spec
	Notify   bool     // announce start and end
	Monitors []string // selected at load time
	Pos      Pos
	Lines    [2]int
}

// Monitor is one check.
type Monitor struct {
	Name        string
	Type        string // HTTP TCP PING UDP PUSH (DNS/NTP/SNMP/SYSLOG normalised to UDP)
	Written     string // type as written (DNS, NTP, ...)
	Target      string
	Title       string
	Description string
	Group       string
	Tags        []Tag

	Interval        time.Duration
	Retry           time.Duration
	Timeout         time.Duration
	Attempts        int
	RecoverAttempts int

	Nodes           []string // nodes that check it (regions expanded); empty = every probing node
	Area            string   // the $SCOPE it is written under, "" when global
	Parents         []string // monitors this one depends on (parent=)
	Device          string   // host it is listed under (device=), "" = its target host
	parentPos       Pos
	Quorum          string
	OnInsufficient  string
	OnFiltered      string
	OnNodeLoss      string
	PartialEscalate time.Duration
	Scope           string // auto | lan | wan
	Policy          string
	Degraded        time.Duration
	CertDays        int
	CertCritical    int
	Authorized      bool
	Disabled        bool
	Share           bool // share=yes: use the result of an identical check of another monitor
	// Region is the region whose nodes check it: RegionGlobal for every
	// node, "" for a list of nodes.
	Region string
	// Mute: no notifications at all; SystemChannel: notified through the
	// system channels instead of the alert channels.
	Mute          bool
	SystemChannel bool
	// Unrouted: its policy has no channels, so it goes to the system ones.
	Unrouted bool

	// Push monitors.
	Period time.Duration
	Grace  time.Duration

	// Opts holds every effective option (after $DEFAULT), including the
	// type-specific ones the probe interprets. References are unresolved.
	Opts map[string]string
	// Refs lists ${secret:...} and ${env:...} references used.
	Refs []string
	UDP  *UDPTarget

	Pos   Pos
	Lines [2]int // statement span

	DefHash, ParamHash, MetaHash string
}

// UDPTarget is the resolved UDP target.
type UDPTarget struct {
	Host     string
	Port     int
	Keyword  string // canonical keyword, "" for raw ports
	Template string
	Depth    keywords.Depth
	Entry    *keywords.Entry
}

// Node is a probe node.
type Node struct {
	Name        string
	Description string
	Probe       bool
	AnchorsLAN  []string
	AnchorsWAN  []string
	NoAnchors   bool
	LocalDetect bool
	Region      string
	Policy      string
	NodeTimeout time.Duration
	// Standby is the address (https://host:port) at which this node can
	// take over as master; it keeps a copy of the master's state.
	Standby string
	Opts    map[string]string
	Pos     Pos
	Lines   [2]int
}

// Channel is a notification channel.
type Channel struct {
	Name    string
	Kind    string
	Opts    map[string]string // kind-specific options (refs unresolved)
	Retries int
	// RetriesSet: retries= was written (0 turns retries off for the channel).
	RetriesSet bool
	Rate       string
	// Period limits the channel to a time period ("!name" = outside it).
	Period string
	// Members are the channels of a "group" channel, all notified at once.
	Members []string
	// System marks a system channel (system=yes): it carries notices about
	// the monitoring itself, and alerts when the usual channels are not
	// set or failed.
	System bool
	// NoReport: the channel does not take the daily status report (report=no).
	NoReport bool
	// ReportSet: report= was written. Without it SMS and voice channels do
	// not take the report.
	ReportSet bool
	Refs      []string
	Pos       Pos
	Lines     [2]int
}

// Policy is a notification policy. Unset numbers are filled from the
// tunables; the raw strings are kept for display.
type Policy struct {
	Name  string
	Chain []string
	// Network receives the summaries of failures that point at the
	// network (a segment, DNS) rather than the services.
	Network          []string
	Mode             string // fallback | all
	Repeat           time.Duration
	Max              int
	Delay            time.Duration
	ReopenWindow     time.Duration
	Recovery         bool
	NotifyDegraded   bool
	AckExpire        time.Duration
	AckClearOnChange bool
	RecoveryAfterAck bool
	Escalate         []Escalation
	Opts             map[string]string
	Implicit         bool // "default"/"system" not written in the file
	// Mute is set on the policy of muted monitors.
	Mute  bool
	Pos   Pos
	Lines [2]int
}

// StatusPage is a public status page.
type StatusPage struct {
	Name    string
	Title   string
	Select  string
	Windows []string
	Path    string
	Logo    string
	Footer  string
	Theme   string
	// Domain serves the page at the root of its own host name.
	Domain string
	// Subscribe names the e-mail channel subscriptions are sent through
	// ("" = no subscriptions).
	Subscribe    string
	subscribePos Pos
	Pos          Pos
	Lines        [2]int
}

// Monitor types and their shorthands.
var monitorTypes = map[string]bool{"HTTP": true, "TCP": true, "PING": true, "UDP": true, "DNS": true, "NTP": true, "SNMP": true, "SYSLOG": true, "PUSH": true, "EXEC": true,
	"REDIS": true, "POSTGRES": true, "MYSQL": true, "GRPC": true, "DOCKER": true, "DOMAIN": true, "APP": true}

// IsMonitorType reports whether t (upper case) is a monitor record type.
func IsMonitorType(t string) bool { return monitorTypes[t] }

// FindMonitor returns a monitor by name.
func (c *Config) FindMonitor(name string) *Monitor {
	if m := monitorIndexOf(c)[name]; m != nil && m.Name == name {
		return m
	}
	// Not in the index (or the index is stale): look through them all, so
	// a miss is never wrong, only slower.
	for _, m := range c.Monitors {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// monitorIndex maps the monitors of one Config by name. Lookups by name
// happen once per monitor in list views; a linear search made those
// quadratic in the number of monitors. The indexes of the last few
// Configs asked about are kept (callers often compare the running and the
// previous configuration in one loop), each rebuilt when its slice of
// monitors was replaced or grew.
type monitorIndex struct {
	cfg   *Config
	first **Monitor
	n     int
	m     map[string]*Monitor
}

const monitorIndexes = 4

var monitorIndexCache atomic.Pointer[[]*monitorIndex]

func monitorIndexOf(c *Config) map[string]*Monitor {
	var first **Monitor
	if len(c.Monitors) > 0 {
		first = &c.Monitors[0]
	}
	var cached []*monitorIndex
	if p := monitorIndexCache.Load(); p != nil {
		cached = *p
	}
	for _, ix := range cached {
		if ix.cfg == c && ix.n == len(c.Monitors) && ix.first == first {
			return ix.m
		}
	}
	ix := &monitorIndex{cfg: c, first: first, n: len(c.Monitors), m: make(map[string]*Monitor, len(c.Monitors))}
	for _, m := range c.Monitors {
		if _, dup := ix.m[m.Name]; !dup {
			ix.m[m.Name] = m
		}
	}
	next := []*monitorIndex{ix}
	for _, old := range cached {
		if old.cfg != c && len(next) < monitorIndexes {
			next = append(next, old)
		}
	}
	monitorIndexCache.Store(&next)
	return ix.m
}

// HasName reports whether a record of any type is called name: names are
// unique across record types.
func (c *Config) HasName(name string) bool {
	if c.FindMonitor(name) != nil || c.FindChannel(name) != nil || c.FindNode(name) != nil {
		return true
	}
	if p := c.FindPolicy(name); p != nil && !p.Implicit {
		return true
	}
	for _, sp := range c.StatusPages {
		if sp.Name == name {
			return true
		}
	}
	return false
}

// FindNode returns a node by name.
func (c *Config) FindNode(name string) *Node {
	for _, n := range c.Nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// RegionGlobal is the region of a monitor every node checks.
const RegionGlobal = "global"

// FindPolicy returns a policy by name.
func (c *Config) FindPolicy(name string) *Policy {
	for _, p := range c.Policies {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// FindPeriod returns the named time period, or nil.
func (c *Config) FindPeriod(name string) *Period {
	for _, p := range c.Periods {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// FindChannel returns a channel by name.
func (c *Config) FindChannel(name string) *Channel {
	for _, ch := range c.Channels {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}

// EscalateText writes escalation steps back in the escalate= form.
func EscalateText(steps []Escalation) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = shortDuration(s.After) + ":" + strings.Join(s.Channels, "+")
	}
	return strings.Join(parts, ",")
}

func shortDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d/time.Second)) + "s"
}
