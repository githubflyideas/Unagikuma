package config

import (
	"strconv"
	"strings"
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
	Teams       []*Team
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
	Share           bool   // share=yes: use the result of an identical check of another monitor
	Team            string // team=: the team that owns it ("" = the built-in admin team)
	// Region is the region whose nodes check it: RegionGlobal for every
	// node, "" for a list of nodes.
	Region string
	// Mute: no notifications at all; SystemChannel: notified through the
	// system channels instead of the team's.
	Mute          bool
	SystemChannel bool
	// Unrouted: its team has no channels, so it goes to the system ones.
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
	Rate    string
	// Period limits the channel to a time period ("!name" = outside it).
	Period string
	// Members are the channels of a "group" channel, all notified at once.
	Members []string
	// System marks a system channel (system=yes): it carries notices about
	// the monitoring itself, and those of teams whose own channels are not
	// set or failed.
	System bool
	// NoReport: the channel does not take the daily status report (report=no).
	NoReport bool
	Refs     []string
	Pos      Pos
	Lines    [2]int
}

// Policy is a notification policy. Unset numbers are filled from the
// tunables; the raw strings are kept for display.
type Policy struct {
	Name  string
	Chain []string
	// Network receives the summaries of failures that point at the
	// network (a segment, DNS) rather than the services: the network team.
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
	// Team is set on the policy made from a team's channels
	// ("team:NAME"); Mute on the policy of muted monitors.
	Team  string
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
	"REDIS": true, "POSTGRES": true, "MYSQL": true, "GRPC": true, "DOCKER": true, "DOMAIN": true}

// IsMonitorType reports whether t (upper case) is a monitor record type.
func IsMonitorType(t string) bool { return monitorTypes[t] }

// FindMonitor returns a monitor by name.
func (c *Config) FindMonitor(name string) *Monitor {
	for _, m := range c.Monitors {
		if m.Name == name {
			return m
		}
	}
	return nil
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

// AdminTeam is the built-in team. It always exists, cannot be deleted or
// renamed, and owns every monitor without team= (team=admin is the same).
const AdminTeam = "admin"

// Team is a group of people who own monitors (team= on a monitor): its
// members see and handle those monitors, with the role of their account
// (administrator, operator or read-only). A person can be in several teams.
type Team struct {
	Name        string
	Title       string
	Description string
	// Members are user names. Files before 2.6 listed them by role
	// (managers=, operators=, viewers=); those lists are read as members.
	Members []string
	// Legacy is set when the record still uses the role lists.
	Legacy bool
	// Channels notify the team's alerts, in order; Mode is fallback (stop
	// at the first that succeeds) or all.
	Channels []string
	Mode     string
	Policy   string // policy= of files before 2.6 ("" = none)
	Implicit bool   // the built-in admin team, not written in the file
	Pos      Pos
	Lines    [2]int
}

// Has reports whether user is a member of the team.
func (t *Team) Has(user string) bool {
	for _, n := range t.Members {
		if n == user {
			return true
		}
	}
	return false
}

// TeamOf is the team that owns a monitor: AdminTeam when it names none.
func (m *Monitor) TeamOf() string {
	if m.Team == "" {
		return AdminTeam
	}
	return m.Team
}

// FindTeam returns the named team, or nil. The built-in admin team is
// always found, written in the file or not.
func (c *Config) FindTeam(name string) *Team {
	for _, t := range c.Teams {
		if t.Name == name {
			return t
		}
	}
	if name == AdminTeam {
		return &Team{Name: AdminTeam, Title: AdminTeam, Mode: "fallback", Implicit: true}
	}
	return nil
}

// AllTeams lists the teams with the built-in admin team first.
func (c *Config) AllTeams() []*Team {
	out := []*Team{c.FindTeam(AdminTeam)}
	for _, t := range c.Teams {
		if t.Name != AdminTeam {
			out = append(out, t)
		}
	}
	return out
}

// MultiTeam reports whether there are teams besides the built-in one.
func (c *Config) MultiTeam() bool {
	for _, t := range c.Teams {
		if t.Name != AdminTeam {
			return true
		}
	}
	return false
}

// TeamsOf lists the teams user is a member of. A user listed in no team
// is in the built-in admin team.
func (c *Config) TeamsOf(user string) []string {
	var out []string
	for _, t := range c.AllTeams() {
		if t.Has(user) {
			out = append(out, t.Name)
		}
	}
	if len(out) == 0 {
		out = []string{AdminTeam}
	}
	return out
}

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
