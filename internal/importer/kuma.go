// Package importer converts other monitoring tools' configuration into
// monitors.conf records, with a report of what was carried over, what
// needs a look and what was left out (and what to do about it).
package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/notify"
)

// Result is a conversion: records to add, secrets to store, and the report.
type Result struct {
	Blocked  bool                `json:"blocked,omitempty"` // bounded adapters: any unsupported item blocks apply
	Records  []config.RecordSpec `json:"-"`
	Secrets  map[string]string   `json:"-"`
	Text     string              `json:"text"` // the records as they will be written
	Monitors int                 `json:"monitors"`
	Channels int                 `json:"channels"`
	Notes    []string            `json:"notes"` // everything not imported as it was, in one line each
	Area     string              `json:"area"`  // $SCOPE section the monitors go to
	Users    []KumaUser          `json:"users"` // accounts found in kuma.db
	Items    []Item              `json:"items"` // the report, item by item
	Info     []string            `json:"info"`  // general remarks for the report
	Source   string              `json:"source"`
	File     string              `json:"file"`
	When     time.Time           `json:"-"`
	// PushTokens are Uptime Kuma push tokens by monitor name: set them
	// once the monitors exist and senders keep working unchanged.
	PushTokens map[string]string `json:"-"`

	sections []section
}

// Options steer an import.
type Options struct {
	// Area is the $SCOPE section imported monitors are written to, e.g.
	// "region:north" ("" = the global section). Channels and policies
	// stay global: they are shared configuration.
	Area string
	// File names the input in the report and the block header.
	File string
	// Now is the import time (default time.Now).
	Now time.Time
	// SecretExists reports a secret name already in use, so imported
	// credentials never overwrite one.
	SecretExists func(name string) bool
	// Resources are Nagios $USERn$ macros (resource.cfg).
	Resources map[string]string
	// UserExists reports an account name already in use.
	UserExists func(name string) bool
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

type kumaBackup struct {
	Version          string             `json:"version"`
	NotificationList []kumaNotification `json:"notificationList"`
	MonitorList      []kumaMonitor      `json:"monitorList"`
	// From kuma.db only.
	Maintenances []kumaMaint      `json:"-"`
	StatusPages  []kumaStatusPage `json:"-"`
	Proxies      map[int64]kumaProxy
	Incidents    int `json:"-"`
	APIKeys      int `json:"-"`
	FromDB       bool
}

type kumaNotification struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Config    string `json:"config"` // JSON object as a string
	Active    flag   `json:"active"`
	IsDefault flag   `json:"isDefault"`
}

type kumaTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type kumaMaint struct {
	ID                                int64
	Title, Description, Strategy      string
	Active                            bool
	StartDate, EndDate                string
	StartTime, EndTime                string
	Weekdays, DaysOfMonth, Cron, Zone string
	IntervalDay, Duration             int
	Monitors                          []int64
}

type kumaStatusPage struct {
	Slug, Title, Description string
	Domains                  []string
	Groups                   []kumaPageGroup
	Password                 bool
}

type kumaPageGroup struct {
	Name     string
	Monitors []int64
}

type kumaProxy struct {
	Protocol, Host string
	Port           int
	Auth           bool
}

type kumaMonitor struct {
	ID               int64           `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Type             string          `json:"type"`
	URL              string          `json:"url"`
	Method           string          `json:"method"`
	Body             string          `json:"body"`
	Headers          string          `json:"headers"`
	Hostname         string          `json:"hostname"`
	Port             int             `json:"port"`
	Interval         int             `json:"interval"`
	RetryInterval    int             `json:"retryInterval"`
	MaxRetries       int             `json:"maxretries"`
	Timeout          float64         `json:"timeout"`
	Active           flag            `json:"active"`
	Keyword          string          `json:"keyword"`
	InvertKeyword    flag            `json:"invertKeyword"`
	JSONPath         string          `json:"jsonPath"`
	JSONOp           string          `json:"jsonPathOperator"`
	ExpectedValue    string          `json:"expectedValue"`
	IgnoreTLS        flag            `json:"ignoreTls"`
	UpsideDown       flag            `json:"upsideDown"`
	MaxRedirects     *int            `json:"maxredirects"`
	AcceptedStatus   []string        `json:"accepted_statuscodes"`
	DNSResolveType   string          `json:"dns_resolve_type"`
	DNSResolveServer string          `json:"dns_resolve_server"`
	DBConn           string          `json:"databaseConnectionString"`
	DockerContainer  string          `json:"docker_container"`
	DockerHost       *int64          `json:"docker_host"`
	GRPCURL          string          `json:"grpcUrl"`
	GRPCService      string          `json:"grpcServiceName"`
	GRPCTLS          flag            `json:"grpcEnableTls"`
	AuthMethod       string          `json:"authMethod"`
	BasicUser        string          `json:"basic_auth_user"`
	BasicPass        string          `json:"basic_auth_pass"`
	Bearer           string          `json:"bearer_token"`
	Parent           *int64          `json:"parent"`
	PathName         string          `json:"pathName"`
	ExpiryNotify     flag            `json:"expiryNotification"`
	Notifications    map[string]bool `json:"notificationIDList"`
	Tags             []kumaTag       `json:"tags"`
	PushToken        string          `json:"pushToken"`
	ProxyID          *int64          `json:"proxyId"`
	SNMPVersion      string          `json:"snmpVersion"`
	SNMPOID          string          `json:"snmpOid"`
	RadiusPassword   string          `json:"radiusPassword"`
}

// flag accepts Kuma's booleans, written as true/false or 1/0.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "true", "1":
		*f = true
	default:
		*f = false
	}
	return nil
}

// kumaImport carries one Uptime Kuma conversion.
type kumaImport struct {
	bk      *kumaBackup
	o       Options
	res     *Result
	names   *namer
	have    map[string]string
	secrets map[string]bool
	byID    map[int64]*kumaMonitor
	monName map[int64]string // Kuma id → record name
}

// Kuma converts an Uptime Kuma database (kuma.db) or JSON backup
// (Settings > Backup > Export in version 1). Names that clash with records
// of existing are renamed; checks that exist already are skipped.
func Kuma(data []byte, existing *config.Config, o Options) (*Result, error) {
	return KumaWithWAL(data, nil, existing, o)
}

// KumaWithWAL is Kuma for a kuma.db copied while Uptime Kuma was running:
// wal is the content of kuma.db-wal next to it (nil when there is none).
func KumaWithWAL(data, wal []byte, existing *config.Config, o Options) (*Result, error) {
	var bk kumaBackup
	var users []KumaUser
	if isSQLite(data) {
		b, u, err := readKumaDB(data, wal)
		if err != nil {
			return nil, err
		}
		bk, users = *b, u
	} else {
		if err := json.Unmarshal(data, &bk); err != nil {
			return nil, errors.New("this file is neither an Uptime Kuma database (kuma.db) nor an Uptime Kuma backup (.json)")
		}
		if bk.MonitorList == nil && bk.NotificationList == nil {
			return nil, errors.New("this JSON file is not an Uptime Kuma backup: it has no monitor list")
		}
	}
	source := "Uptime Kuma"
	if bk.Version != "" {
		source += " " + bk.Version
	}
	res := &Result{Secrets: map[string]string{}, Area: o.Area, Users: users, Source: source, File: o.File, When: o.now(), PushTokens: map[string]string{}}
	k := &kumaImport{bk: &bk, o: o, res: res, names: newNamer(existing), have: existingChecks(existing), secrets: map[string]bool{},
		byID: map[int64]*kumaMonitor{}, monName: map[int64]string{}}
	for i := range bk.MonitorList {
		k.byID[bk.MonitorList[i].ID] = &bk.MonitorList[i]
	}
	k.convert(existing)
	if !isSQLite(data) {
		res.Info = append(res.Info, "A JSON backup has no accounts, maintenance windows or status pages. To bring those too, import kuma.db instead (it is in Uptime Kuma's data folder).")
	}
	if res.count("", StatusImported, StatusAttention, StatusSkipped) == 0 && len(users) == 0 {
		if len(bk.MonitorList) == 0 {
			return nil, errors.New("this Uptime Kuma data has no monitors")
		}
	}
	res.finish()
	return res, nil
}

// groupPath is a monitor's group as "Parent / Child" ("" when none).
func (k *kumaImport) groupPath(m *kumaMonitor) string {
	var parts []string
	seen := map[int64]bool{}
	for p := m.Parent; p != nil && !seen[*p]; {
		seen[*p] = true
		g := k.byID[*p]
		if g == nil {
			break
		}
		parts = append([]string{strings.TrimSpace(g.Name)}, parts...)
		p = g.Parent
	}
	return strings.Join(parts, " / ")
}

func (k *kumaImport) convert(existing *config.Config) {
	c := &kumaConvert{k: k, existing: existing, used: map[int64]bool{}, usedBySkipped: map[int64]bool{},
		groupsUsed: map[string]bool{}, chanName: map[int64]string{}}
	ordered := k.orderedMonitors()
	for i := range ordered {
		if ordered[i].Type != "group" {
			c.monitor(&ordered[i])
		}
	}
	c.emptyGroups()
	c.channels()
	c.policies()
	c.sections()
	k.maintenances(existing)
	k.statusPages(existing)
	k.accounts()
	res := k.res
	res.Info = append(res.Info,
		"Past checks, uptime history and incidents stay in Uptime Kuma: history starts fresh here.",
		"Credentials (tokens, passwords, webhook addresses) are stored as encrypted secrets, not in the configuration file.")
	if k.bk.APIKeys > 0 || k.bk.Incidents > 0 {
		res.Info = append(res.Info, "API keys, status page incidents, settings and two-factor login are not carried over.")
	}
}

// orderedMonitors sorts the monitors by group, monitors outside groups
// last, then by name.
func (k *kumaImport) orderedMonitors() []kumaMonitor {
	ordered := append([]kumaMonitor(nil), k.bk.MonitorList...)
	sort.SliceStable(ordered, func(i, j int) bool {
		gi, gj := k.groupPath(&ordered[i]), k.groupPath(&ordered[j])
		if gi != gj {
			if gi == "" || gj == "" {
				return gj == "" // monitors outside groups last
			}
			return gi < gj
		}
		return strings.ToLower(ordered[i].Name) < strings.ToLower(ordered[j].Name)
	})
	return ordered
}

// kumaConvert carries one conversion from monitors to channels, policies
// and sections.
type kumaConvert struct {
	k                   *kumaImport
	existing            *config.Config
	mons                []kumaPending
	used, usedBySkipped map[int64]bool
	groupsUsed          map[string]bool
	chanName            map[int64]string
	chans, policyRecs   []config.RecordSpec
}

type kumaPending struct {
	spec  *config.RecordSpec
	item  int
	set   []int64
	group string
}

func notificationIDs(m *kumaMonitor) []int64 {
	var set []int64
	for id, on := range m.Notifications {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && on {
			set = append(set, n)
		}
	}
	sort.Slice(set, func(a, b int) bool { return set[a] < set[b] })
	return set
}

func (c *kumaConvert) monitor(m *kumaMonitor) {
	k, res := c.k, c.k.res
	group := k.groupPath(m)
	it := Item{Kind: "monitor", Source: m.Name, Type: kumaTypeLabel(m.Type), Target: kumaTarget(m), Group: group, Status: StatusImported}
	spec := k.monitorSpec(m, &it)
	if spec == nil {
		it.Status = StatusLeftOut
		res.add(it)
		return
	}
	key := checkKey(spec.Type, spec.Target, optMap(spec.Opts))
	if spec.Type == "PUSH" {
		key = "PUSH|" + strings.ToLower(m.Name) // nothing to compare but the name
	}
	if prev, dup := k.have[key]; dup {
		c.duplicate(m, spec, it, group, prev)
		return
	}
	k.have[key] = spec.Name
	k.monName[m.ID] = spec.Name
	if group != "" {
		spec.Opts = append(spec.Opts, config.Opt{Key: "group", Value: group})
		c.groupsUsed[group] = true
	}
	if !m.Active {
		spec.Opts = append(spec.Opts, config.Opt{Key: "disabled", Value: "yes"})
		it.attention("It was paused in Uptime Kuma, so it is imported paused.", "Resume it when you want it checked.")
	}
	set := notificationIDs(m)
	for _, n := range set {
		c.used[n] = true
	}
	it.Name = spec.Name
	res.add(it)
	c.mons = append(c.mons, kumaPending{spec, len(res.Items) - 1, set, group})
}

// duplicate records a monitor that checks the same thing as prev.
func (c *kumaConvert) duplicate(m *kumaMonitor, spec *config.RecordSpec, it Item, group, prev string) {
	k := c.k
	k.names.release(spec.Name)
	delete(k.res.PushTokens, spec.Name)
	k.monName[m.ID] = prev
	if group != "" {
		c.groupsUsed[group] = true
	}
	for _, n := range notificationIDs(m) {
		c.usedBySkipped[n] = true
	}
	dup := Item{Kind: "monitor", Source: m.Name, Name: prev, Type: it.Type, Target: it.Target, Group: group, Status: StatusSkipped,
		Why: "The same check is already here as " + prev + "."}
	if c.existing == nil || !c.existing.HasName(prev) {
		dup.Status, dup.Why = StatusMerged, "It checks the same thing as "+prev+", so it was imported once, as "+prev+"."
	}
	k.res.add(dup)
}

// emptyGroups lists the groups with nothing to carry over.
func (c *kumaConvert) emptyGroups() {
	k := c.k
	for _, m := range k.bk.MonitorList {
		if m.Type != "group" {
			continue
		}
		path := k.groupPath(&m)
		if path != "" {
			path += " / "
		}
		path += strings.TrimSpace(m.Name)
		hasChild := false
		for g := range c.groupsUsed {
			if g == path || strings.HasPrefix(g, path+" / ") {
				hasChild = true
			}
		}
		if !hasChild {
			k.res.add(Item{Kind: "group", Source: path, Status: StatusLeftOut, Why: "The group has no monitors that were imported.",
				Todo: "Nothing to do: groups appear here as soon as a monitor uses them (group=)."})
		}
	}
}

// channels converts the channels the imported monitors use and lists the
// others.
func (c *kumaConvert) channels() {
	k, res := c.k, c.k.res
	var unused []string
	unusedName := ""
	for _, n := range k.bk.NotificationList {
		switch {
		case !c.used[n.ID] && c.usedBySkipped[n.ID]:
			res.add(Item{Kind: "channel", Source: n.Name, Type: kumaChannelLabel(n), Status: StatusSkipped,
				Why: "Only monitors that are already here use it; they keep their notifications."})
		case !c.used[n.ID]:
			unused = append(unused, n.Name+" ("+kumaChannelLabel(n)+")")
			unusedName = n.Name
		default:
			c.channel(n)
		}
	}
	if len(unused) == 0 {
		return
	}
	it := Item{Kind: "channel", Source: unusedName, Status: StatusLeftOut,
		Why: "No imported monitor uses it.", Todo: "Add it under Alerts & notifications › Channels if you still need it."}
	if len(unused) > 1 {
		it.Kind, it.Source = "channels", strings.Join(unused, ", ")
		it.Why, it.Todo = "No imported monitor uses them.", "Add them under Alerts & notifications › Channels if you still need them."
	}
	res.add(it)
}

func (c *kumaConvert) channel(n kumaNotification) {
	res := c.k.res
	it := Item{Kind: "channel", Source: n.Name, Type: kumaChannelLabel(n), Status: StatusImported}
	spec := c.k.channel(n, c.existing, &it)
	if spec == nil {
		if it.Status == StatusImported {
			it.Status = StatusLeftOut
		}
		res.add(it)
		return
	}
	c.chanName[n.ID] = spec.Name
	it.Name = spec.Name
	if it.Status != StatusSkipped {
		c.chans = append(c.chans, *spec)
		res.Channels++
	}
	res.add(it)
}

// policies gives each imported monitor the policy of its set of channels:
// one per distinct set, named after them.
func (c *kumaConvert) policies() {
	k, res := c.k, c.k.res
	policyOf := map[string]string{}
	for _, pm := range c.mons {
		var set, lost []string
		for _, id := range pm.set {
			if c.chanName[id] != "" {
				set = append(set, c.chanName[id])
				continue
			}
			for _, n := range k.bk.NotificationList {
				if n.ID == id {
					lost = append(lost, n.Name)
				}
			}
		}
		if len(lost) > 0 {
			res.Items[pm.item].attention("Notification "+strings.Join(lost, ", ")+" could not be imported (see below).", "")
		}
		sort.Strings(set)
		if len(set) > 0 {
			key := strings.Join(set, ",")
			p, ok := policyOf[key]
			if !ok {
				p = k.policy(set, c.existing, &c.policyRecs)
				policyOf[key] = p
			}
			pm.spec.Opts = append(pm.spec.Opts, config.Opt{Key: "policy", Value: p})
		} else if len(pm.set) == 0 {
			res.Items[pm.item].Why = joinSentences(res.Items[pm.item].Why, "")
		}
		pm.spec.Area = k.o.Area
	}
}

// sections orders the records: shared ones first, then the monitors by
// group.
func (c *kumaConvert) sections() {
	res := c.k.res
	sortSection(c.chans)
	res.sections = append(res.sections, section{title: "Notification channels (from Uptime Kuma notifications)", recs: c.chans, shared: true})
	res.sections = append(res.sections, section{title: "Notification policies: the channels each monitor notifies", recs: c.policyRecs, shared: true})
	var cur *section
	for _, pm := range c.mons {
		title := "Uptime Kuma group: " + pm.group
		if pm.group == "" {
			title = "Not in a group"
		}
		if cur == nil || cur.title != title {
			res.sections = append(res.sections, section{title: title})
			cur = &res.sections[len(res.sections)-1]
		}
		cur.recs = append(cur.recs, *pm.spec)
		res.Monitors++
	}
}

// policy creates a POLICY record for a set of channels.
func (k *kumaImport) policy(set []string, existing *config.Config, out *[]config.RecordSpec) string {
	chain := strings.Join(set, ",")
	if existing != nil {
		for _, p := range existing.Policies {
			if strings.Join(p.Chain, ",") == chain && p.Mode == "all" && strings.HasPrefix(p.Name, "notify-") {
				return p.Name
			}
		}
	}
	base := "notify-" + strings.Join(set, "-")
	if len(base) > 48 {
		base = "notify-" + set[0] + "-and-" + strconv.Itoa(len(set)-1) + "-more"
	}
	name := k.names.take(base, "notify")
	*out = append(*out, config.RecordSpec{Name: name, Type: "POLICY", Opts: []config.Opt{{Key: "chain", Value: chain}, {Key: "mode", Value: "all"}}})
	sortSection(*out)
	k.res.add(Item{Kind: "policy", Source: name, Name: name, Target: "sends to " + strings.Join(set, ", "), Status: StatusImported})
	return name
}

var kumaTypeLabels = map[string]string{
	"http": "HTTP", "keyword": "HTTP keyword", "json-query": "HTTP JSON", "port": "TCP port", "ping": "Ping", "dns": "DNS",
	"push": "Push", "docker": "Docker", "mysql": "MySQL", "postgres": "PostgreSQL", "redis": "Redis", "grpc-keyword": "gRPC",
	"mqtt": "MQTT", "gamedig": "Game server", "steam": "Steam game server", "mongodb": "MongoDB", "sqlserver": "SQL Server",
	"radius": "RADIUS", "tailscale-ping": "Tailscale ping", "real-browser": "Browser", "kafka-producer": "Kafka",
	"snmp": "SNMP", "smtp": "SMTP", "websocket-upgrade": "WebSocket", "rabbitmq": "RabbitMQ", "globalping": "Globalping",
	"group": "Group", "manual": "Manual", "system-service": "System service",
}

func kumaTypeLabel(t string) string {
	if l, ok := kumaTypeLabels[t]; ok {
		return l
	}
	return t
}

// kumaTarget says what a Kuma monitor checks, for the report.
func kumaTarget(m *kumaMonitor) string {
	switch {
	case m.Type == "push":
		return "waits for a push"
	case m.Type == "dns":
		return m.Hostname
	case m.URL != "" && m.URL != "https://" && m.URL != "http://":
		return m.URL
	case m.Type == "grpc-keyword" && m.GRPCURL != "":
		return m.GRPCURL
	case m.Type == "docker":
		return "container " + m.DockerContainer
	case m.DBConn != "":
		return redactConn(m.DBConn)
	case m.Hostname != "" && m.Port > 0:
		return net.JoinHostPort(m.Hostname, strconv.Itoa(m.Port))
	}
	return m.Hostname
}

// redactConn hides the password of a connection string.
func redactConn(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		u.User = nil
		return u.String()
	}
	if i := strings.Index(strings.ToLower(s), "password="); i >= 0 {
		return s[:i] + "Password=…"
	}
	return s
}

// unsupported says why a Kuma monitor type has no equivalent and what to
// use instead.
var unsupported = map[string][2]string{
	"mqtt":              {"Unagikuma has no MQTT check.", "Check the broker port with a TCP monitor, or have the device call a push URL (a Push monitor)."},
	"gamedig":           {"Unagikuma has no game-server query.", "Check the server's port with a TCP or UDP monitor."},
	"steam":             {"Unagikuma has no Steam server query.", "Check the server's port with a TCP or UDP monitor."},
	"mongodb":           {"Unagikuma has no MongoDB check.", "Check port 27017 with a TCP monitor, or run a script check (EXEC)."},
	"sqlserver":         {"Unagikuma has no SQL Server check.", "Check port 1433 with a TCP monitor, or run a script check (EXEC)."},
	"radius":            {"Unagikuma cannot log in to RADIUS.", "Check that the server answers with a UDP monitor on port radius (1812)."},
	"kafka-producer":    {"Unagikuma cannot send Kafka messages.", "Check the broker port with a TCP monitor."},
	"websocket-upgrade": {"Unagikuma has no WebSocket check.", "Check the server with an HTTP or TCP monitor."},
	"rabbitmq":          {"Unagikuma has no RabbitMQ check.", "Check the management API with an HTTP monitor, or port 5672 with a TCP monitor."},
	"globalping":        {"Globalping is an outside service that Unagikuma does not use.", "Add probe nodes in the places you want to check from (Settings › Nodes)."},
	"manual":            {"A manual monitor has nothing to check.", "Use a Push monitor, or announce work under Notices & status."},
	"system-service":    {"Unagikuma does not watch system services directly.", "Run a script check (EXEC), for example check_procs, or check the service's port."},
}

func (k *kumaImport) secretName(rec, key string) string {
	base := "kuma_" + strings.ReplaceAll(rec, "-", "_") + "_" + strings.ReplaceAll(key, "-", "_")
	base = strings.ReplaceAll(base, ".", "_")
	name := base
	for i := 2; k.secrets[name] || (k.o.SecretExists != nil && k.o.SecretExists(name)); i++ {
		name = base + "_" + strconv.Itoa(i)
	}
	k.secrets[name] = true
	return name
}

func (k *kumaImport) secret(rec, key, value string) string {
	n := k.secretName(rec, key)
	k.res.Secrets[n] = value
	return "${secret:" + n + "}"
}

// fallbackName names a monitor whose name has no usable letters (Chinese,
// emoji): after the host it checks, or its type.
func fallbackName(m *kumaMonitor) string {
	for _, s := range []string{m.URL, m.GRPCURL, m.DBConn} {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	if m.Hostname != "" {
		return m.Hostname
	}
	return strings.ReplaceAll(m.Type, "json-query", "http")
}

func (k *kumaImport) monitorSpec(m *kumaMonitor, it *Item) *config.RecordSpec {
	if why, ok := unsupported[m.Type]; ok {
		it.Why, it.Todo = why[0], why[1]
		return nil
	}
	if m.UpsideDown {
		it.Why = "It is in upside-down mode (it is up when the service is down). Unagikuma has no such mode, so it would report the opposite."
		it.Todo = "If you still need it, add it by hand as a normal check and decide what “up” should mean."
		return nil
	}
	b := &kumaSpec{k: k, m: m, it: it}
	b.name = k.names.take(m.Name, fallbackName(m))
	b.spec = &config.RecordSpec{Name: b.name}
	if !b.byType() {
		return nil
	}
	b.common()
	b.spec.Opts = b.opts
	return b.spec
}

// kumaSpec builds the record of one Uptime Kuma monitor.
type kumaSpec struct {
	k    *kumaImport
	m    *kumaMonitor
	it   *Item
	name string
	spec *config.RecordSpec
	opts []config.Opt
}

func (b *kumaSpec) add(key, v string) {
	if v != "" {
		b.opts = append(b.opts, config.Opt{Key: key, Value: v})
	}
}

// drop gives the name back and says why the monitor is not imported.
func (b *kumaSpec) drop(why, todo string) bool {
	b.k.names.release(b.name)
	b.it.Why, b.it.Todo = why, todo
	return false
}

func (b *kumaSpec) target(typ, target string) {
	b.spec.Type, b.spec.Target = typ, target
}

// byType fills the type, target and type-specific options; false means the
// monitor is not imported.
func (b *kumaSpec) byType() bool {
	m := b.m
	switch m.Type {
	case "http", "keyword", "json-query", "real-browser":
		return b.http()
	case "port":
		b.target("TCP", net.JoinHostPort(m.Hostname, strconv.Itoa(m.Port)))
	case "smtp":
		b.target("TCP", net.JoinHostPort(m.Hostname, strconv.Itoa(max(m.Port, 25))))
		b.it.Type = "TCP port"
		b.it.attention("Imported as a check that the mail port answers; the SMTP conversation is not checked.", "")
	case "ping":
		b.target("PING", m.Hostname)
	case "tailscale-ping":
		b.target("PING", m.Hostname)
		b.it.Type = "Ping"
		b.it.attention("It is pinged directly, not through Tailscale.", "Run Unagikuma (or a probe node) on a machine in your tailnet if the name only resolves there.")
	case "snmp":
		b.snmp()
	case "dns":
		b.dns()
	case "postgres", "mysql", "redis":
		return b.database()
	case "docker":
		return b.docker()
	case "grpc-keyword":
		return b.grpc()
	case "push":
		b.push()
	default:
		return b.drop("Unagikuma has no “"+m.Type+"” check.", "Add a similar check by hand (HTTP, TCP, ping or a script check).")
	}
	return true
}

func (b *kumaSpec) http() bool {
	m, it := b.m, b.it
	if m.URL == "" || m.URL == "https://" || m.URL == "http://" {
		return b.drop("It has no URL.", "Add it by hand with the address to check.")
	}
	b.target("HTTP", m.URL)
	it.Type = "HTTP"
	if m.Method != "" && !strings.EqualFold(m.Method, "GET") {
		b.add("method", strings.ToUpper(m.Method))
	}
	b.add("body", m.Body)
	if len(m.AcceptedStatus) > 0 && !(len(m.AcceptedStatus) == 1 && m.AcceptedStatus[0] == "200-299") {
		b.add("expect", strings.Join(m.AcceptedStatus, ","))
	}
	switch m.Type {
	case "keyword":
		b.add("keyword", m.Keyword)
		if m.InvertKeyword {
			b.add("absent", "yes")
		}
		it.Type = "HTTP keyword"
	case "json-query":
		it.Type = "HTTP JSON"
		b.add("json-path", m.JSONPath)
		b.jsonCompare()
	case "real-browser":
		it.attention("Uptime Kuma loaded it in a browser; here it is a plain HTTP check (no JavaScript).", "")
	}
	if m.IgnoreTLS {
		b.add("insecure", "yes")
	}
	if m.MaxRedirects != nil && *m.MaxRedirects == 0 {
		b.add("no-follow", "yes")
	}
	if bool(m.ExpiryNotify) && strings.HasPrefix(m.URL, "https://") {
		b.add("cert-days", "62")
	}
	b.httpAuth()
	b.httpHeaders()
	b.httpProxy()
	return true
}

func (b *kumaSpec) jsonCompare() {
	m := b.m
	switch op := m.JSONOp; op {
	case "", "==":
		b.add("json-expect", m.ExpectedValue)
		return
	case ">", ">=", "<", "<=":
		if _, err := strconv.ParseFloat(m.ExpectedValue, 64); err == nil {
			b.add("json-expect-num", op+m.ExpectedValue)
			return
		}
	}
	b.it.attention("The JSON comparison “"+m.JSONOp+" "+m.ExpectedValue+"” has no equivalent; only the path is checked.", "Set json-expect= or json-expect-num= on it.")
}

func (b *kumaSpec) httpAuth() {
	m := b.m
	switch {
	case m.BasicUser != "" && (m.AuthMethod == "" || m.AuthMethod == "basic"):
		b.add("basic", b.k.secret(b.name, "basic", m.BasicUser+":"+m.BasicPass))
	case m.AuthMethod == "bearer" && m.Bearer != "":
		b.add("bearer", b.k.secret(b.name, "bearer", m.Bearer))
	case m.AuthMethod != "" && m.AuthMethod != "basic" && m.AuthMethod != "bearer":
		b.it.attention("Its "+m.AuthMethod+" login was not carried over.", "Add the login by hand (basic=, bearer= or a header).")
	}
}

func (b *kumaSpec) httpHeaders() {
	raw := b.m.Headers
	if raw == "" || raw == "{}" || raw == "null" {
		return
	}
	var h map[string]any
	if json.Unmarshal([]byte(raw), &h) != nil {
		b.it.attention("Its request headers are not valid JSON and were left out.", "Add them by hand as header.NAME=value.")
		return
	}
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, hk := range keys {
		v := fmt.Sprint(h[hk])
		hkey := "header." + strings.ToLower(hk)
		switch {
		case !optKeyOK(hkey):
			b.it.attention("Header “"+hk+"” has a name that cannot be written here.", "Add it by hand.")
		case isCredentialHeader(hk):
			b.add(hkey, b.k.secret(b.name, strings.ToLower(hk), v))
		default:
			b.add(hkey, v)
		}
	}
}

func (b *kumaSpec) httpProxy() {
	if b.m.ProxyID == nil {
		return
	}
	p, ok := b.k.bk.Proxies[*b.m.ProxyID]
	if !ok || p.Host == "" {
		return
	}
	b.add("proxy", p.Protocol+"://"+net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
	if p.Auth {
		b.it.attention("Its proxy needs a login, which was not carried over.", "Put the login in the proxy URL (http://user:password@host:port).")
	}
}

func (b *kumaSpec) snmp() {
	m := b.m
	b.target("SNMP", m.Hostname)
	if m.Port > 0 && m.Port != 161 {
		b.spec.Target = net.JoinHostPort(m.Hostname, strconv.Itoa(m.Port))
	}
	if m.RadiusPassword != "" { // Uptime Kuma keeps the community there
		b.add("community", b.k.secret(b.name, "community", m.RadiusPassword))
	}
	b.it.attention("It checks that the device answers SNMP; the OID value comparison is not carried over.", "")
}

func (b *kumaSpec) dns() {
	m := b.m
	server := m.DNSResolveServer
	if server == "" {
		server = "1.1.1.1"
		b.it.attention("Uptime Kuma asked the system's resolver; this check asks 1.1.1.1.", "Change the target to your own DNS server if that is what you meant to watch.")
	}
	if i := strings.Index(server, ","); i >= 0 {
		server = strings.TrimSpace(server[:i])
	}
	b.target("DNS", server)
	if m.Port > 0 && m.Port != 53 {
		b.spec.Target = net.JoinHostPort(server, strconv.Itoa(m.Port))
	}
	b.add("query", m.Hostname)
	if m.DNSResolveType != "" && m.DNSResolveType != "A" {
		b.add("qtype", m.DNSResolveType)
	}
}

func (b *kumaSpec) database() bool {
	m := b.m
	typ, target, extra, password, why := fromConnString(m.Type, m.DBConn, m.Hostname, m.Port)
	if why != "" {
		return b.drop("Its "+why+".", "Add it by hand.")
	}
	b.target(typ, target)
	for _, kv := range extra {
		b.add(kv[0], kv[1])
	}
	if password != "" {
		b.add("password", b.k.secret(b.name, "password", password))
	}
	return true
}

func (b *kumaSpec) docker() bool {
	m := b.m
	if m.DockerContainer == "" {
		return b.drop("It names no container.", "Add it by hand.")
	}
	b.target("DOCKER", m.DockerContainer)
	if m.DockerHost != nil {
		b.it.attention("It is checked through the Docker socket of the machine that runs the check, not a remote Docker host.", "Run a probe node on the Docker host if the container is elsewhere.")
	}
	return true
}

func (b *kumaSpec) grpc() bool {
	m := b.m
	target := strings.TrimPrefix(strings.TrimPrefix(m.GRPCURL, "https://"), "http://")
	if target == "" {
		return b.drop("It has no gRPC address.", "Add it by hand.")
	}
	b.target("GRPC", target)
	b.add("service", m.GRPCService)
	if m.GRPCTLS {
		b.add("tls", "yes")
	}
	b.it.attention("It uses the standard gRPC health check instead of a keyword match.", "")
	return true
}

func (b *kumaSpec) push() {
	m := b.m
	b.spec.Type = "PUSH"
	b.add("period", humanDur(max(m.Interval, 20)))
	if m.PushToken != "" {
		b.k.res.PushTokens[b.name] = m.PushToken
	} else {
		b.it.attention("Its push URL changes.", "Point the sender at the push URL shown on the monitor.")
	}
}

// common adds what every monitor carries: title, description, tags and
// the timing.
func (b *kumaSpec) common() {
	m := b.m
	desc := strings.TrimSpace(m.Description)
	if b.name != m.Name {
		title := strings.TrimSpace(m.Name)
		if r := []rune(title); len(r) > 64 {
			title = string(r[:63]) + "…"
			if desc == "" {
				desc = strings.TrimSpace(m.Name)
			}
		}
		b.add("title", title)
	}
	b.add("description", desc)
	if tags := kumaTags(m.Tags); tags != "" {
		b.add("tags", tags)
	}
	if m.Type == "push" {
		return
	}
	if m.Interval > 0 {
		b.spec.Interval = humanDur(m.Interval)
	}
	if m.RetryInterval > 0 && m.RetryInterval != m.Interval {
		b.add("retry", humanDur(m.RetryInterval))
	}
	b.add("attempts", strconv.Itoa(m.MaxRetries+1))
	if m.Timeout > 0 {
		t := m.Timeout
		if m.Interval > 0 && t >= float64(m.Interval) {
			t = float64(m.Interval) * 0.8
		}
		b.add("timeout", strconv.FormatFloat(t, 'f', -1, 64)+"s")
	}
}

func kumaTags(tags []kumaTag) string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		tag := slug(t.Name)
		if tag == "" {
			continue
		}
		if v := strings.Join(strings.Fields(strings.ReplaceAll(t.Value, ",", " ")), " "); v != "" {
			tag += ":" + v
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

var optKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)

func optKeyOK(k string) bool { return optKeyRe.MatchString(k) }

func isCredentialHeader(h string) bool {
	h = strings.ToLower(h)
	return h == "authorization" || h == "cookie" || h == "proxy-authorization" || strings.Contains(h, "token") ||
		strings.Contains(h, "key") || strings.Contains(h, "secret") || strings.Contains(h, "auth")
}

var kumaChannelLabels = map[string]string{
	"telegram": "Telegram", "slack": "Slack", "discord": "Discord", "webhook": "Webhook", "feishu": "Feishu", "DingDing": "DingTalk",
	"WeCom": "WeCom", "smtp": "Email", "ntfy": "ntfy", "gotify": "Gotify", "pushover": "Pushover", "teams": "Microsoft Teams",
	"PagerDuty": "PagerDuty", "matrix": "Matrix", "mattermost": "Mattermost", "GoogleChat": "Google Chat", "Bark": "Bark",
	"ServerChan": "ServerChan", "PushPlus": "PushPlus", "Opsgenie": "Opsgenie",
}

func kumaChannelLabel(n kumaNotification) string {
	var c map[string]any
	_ = json.Unmarshal([]byte(n.Config), &c)
	t, _ := c["type"].(string)
	if l, ok := kumaChannelLabels[t]; ok {
		return l
	}
	return t
}

// channel maps a Kuma notification. Credentials go to secrets, the record
// refers to them. A channel with the same name and kind that exists
// already is reused.
func (k *kumaImport) channel(n kumaNotification, existing *config.Config, it *Item) *config.RecordSpec {
	var c map[string]any
	if err := json.Unmarshal([]byte(n.Config), &c); err != nil {
		it.Why, it.Todo = "Its settings could not be read.", "Add it by hand under Alerts & notifications › Channels."
		return nil
	}
	ch := &kumaChannel{k: k, c: c, it: it}
	typ := ch.str("type")
	kind := kumaChannelKinds[typ]
	if kind == "" {
		it.Why = "Unagikuma cannot send to “" + typ + "”."
		it.Todo = "Pick a similar channel under Alerts & notifications › Channels (a webhook reaches most services), then add it to the monitor's policy."
		return nil
	}
	if name := slug(n.Name); existing != nil && name != "" {
		if have := existing.FindChannel(name); have != nil && channelKind(have) == kind {
			it.Status, it.Name = StatusSkipped, name
			it.Why = "A " + it.Type + " channel named " + name + " is already here; the imported monitors use it."
			return &config.RecordSpec{Name: name}
		}
	}
	ch.name = k.names.take(n.Name, strings.TrimPrefix(kind, "webhook:"))
	if !ch.fill(typ) {
		return nil
	}
	if !n.Active {
		it.attention("It was turned off in Uptime Kuma; here it is on.", "Remove it from the policy if it should stay quiet.")
	}
	it.Target = channelTarget(kind, ch.opts)
	if p, ok := strings.CutPrefix(kind, "webhook:"); ok {
		// Presets are webhooks with preset= in the file.
		return &config.RecordSpec{Name: ch.name, Type: "CHANNEL", Target: "webhook", Opts: append([]config.Opt{{Key: "preset", Value: p}}, ch.opts...)}
	}
	return &config.RecordSpec{Name: ch.name, Type: "CHANNEL", Target: kind, Opts: ch.opts}
}

// kumaChannelKinds maps Uptime Kuma notification types to channel kinds.
var kumaChannelKinds = map[string]string{"telegram": "telegram", "slack": "slack", "discord": "discord", "webhook": "webhook", "feishu": "feishu",
	"DingDing": "dingtalk", "WeCom": "wecom", "smtp": "email", "ntfy": "webhook:ntfy", "gotify": "webhook:gotify", "pushover": "webhook:pushover",
	"teams": "webhook:teams", "PagerDuty": "webhook:pagerduty", "matrix": "webhook:matrix", "mattermost": "webhook:mattermost",
	"GoogleChat": "webhook:googlechat", "Bark": "webhook:bark", "ServerChan": "webhook:serverchan", "PushPlus": "webhook:pushplus",
	"Opsgenie": "webhook:opsgenie"}

// kumaField is one setting copied as it is: to option key, from Kuma field.
type kumaField struct {
	key, field       string
	secret, required bool
}

// kumaPlainChannels are the kinds whose settings are copied field by field.
var kumaPlainChannels = map[string][]kumaField{
	"discord":    {{"url", "discordWebhookUrl", true, true}},
	"feishu":     {{"url", "feishuWebHookUrl", true, true}},
	"DingDing":   {{"url", "webHookUrl", true, true}, {"secret", "secretKey", true, false}},
	"pushover":   {{"token", "pushoverapptoken", true, true}, {"user-key", "pushoveruserkey", true, true}},
	"PagerDuty":  {{"routing-key", "pagerdutyIntegrationKey", true, true}},
	"mattermost": {{"url", "mattermostWebhookUrl", true, true}},
	"GoogleChat": {{"url", "googleChatWebhookURL", true, true}},
	"ServerChan": {{"sendkey", "serverChanSendKey", true, true}},
	"PushPlus":   {{"token", "pushPlusSendKey", true, true}},
}

// kumaChannel builds the record of one Uptime Kuma notification.
type kumaChannel struct {
	k    *kumaImport
	c    map[string]any
	it   *Item
	name string
	opts []config.Opt
}

func (ch *kumaChannel) str(key string) string {
	switch v := ch.c[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (ch *kumaChannel) secret(key, value string) {
	if value != "" {
		ch.opts = append(ch.opts, config.Opt{Key: key, Value: ch.k.secret(ch.name, key, value)})
	}
}

func (ch *kumaChannel) plain(key, value string) {
	if value != "" {
		ch.opts = append(ch.opts, config.Opt{Key: key, Value: value})
	}
}

// missing reports the first empty field among fields, and gives the name
// back.
func (ch *kumaChannel) missing(fields ...string) bool {
	for _, f := range fields {
		if ch.str(f) == "" {
			ch.drop("Its settings are incomplete ("+f+" is empty).", "Add it by hand under Alerts & notifications › Channels.")
			return true
		}
	}
	return false
}

func (ch *kumaChannel) drop(why, todo string) {
	ch.k.names.release(ch.name)
	ch.it.Why, ch.it.Todo = why, todo
}

// fill copies the settings of a notification of type typ; false means it
// is not imported.
func (ch *kumaChannel) fill(typ string) bool {
	if fields, ok := kumaPlainChannels[typ]; ok {
		var need []string
		for _, f := range fields {
			if f.required {
				need = append(need, f.field)
			}
		}
		if ch.missing(need...) {
			return false
		}
		for _, f := range fields {
			if f.secret {
				ch.secret(f.key, ch.str(f.field))
			} else {
				ch.plain(f.key, ch.str(f.field))
			}
		}
		return true
	}
	fill, ok := map[string]func() bool{
		"telegram": ch.telegram, "slack": ch.slack, "webhook": ch.webhook, "WeCom": ch.wecom, "smtp": ch.smtp,
		"ntfy": ch.ntfy, "gotify": ch.gotify, "teams": ch.teams, "matrix": ch.matrix, "Bark": ch.bark, "Opsgenie": ch.opsgenie,
	}[typ]
	return !ok || fill()
}

func (ch *kumaChannel) telegram() bool {
	if ch.missing("telegramBotToken", "telegramChatID") {
		return false
	}
	ch.secret("token", ch.str("telegramBotToken"))
	ch.plain("chat", ch.str("telegramChatID"))
	if s := ch.str("telegramServerUrl"); s != "" && s != "https://api.telegram.org" {
		ch.plain("api", s)
	}
	return true
}

func (ch *kumaChannel) slack() bool {
	if ch.missing("slackwebhookURL") {
		return false
	}
	ch.secret("url", ch.str("slackwebhookURL"))
	if ch.str("slackchannel") != "" {
		ch.it.attention("Messages go to the webhook's own channel; the channel override ("+ch.str("slackchannel")+") is not used.", "")
	}
	return true
}

func (ch *kumaChannel) webhook() bool {
	if ch.missing("webhookURL") {
		return false
	}
	if u, err := url.Parse(ch.str("webhookURL")); err == nil && (u.RawQuery != "" || u.User != nil) {
		ch.secret("url", ch.str("webhookURL")) // a token in the address
	} else {
		ch.plain("url", ch.str("webhookURL"))
	}
	if ch.str("webhookContentType") == "form-data" {
		ch.plain("content-type", "form")
	}
	switch body := ch.str("webhookCustomBody"); {
	case ch.str("webhookContentType") == "custom" && body != "" && notify.ValidateTemplate(oneLine(body)) == nil:
		// Kuma's template (Liquid, monitorJSON, heartbeatJSON, msg) is
		// read the Kuma way, so the body is carried over as it is.
		ch.plain("content-type", "json")
		ch.plain("body", oneLine(body))
	case ch.str("webhookContentType") == "custom":
		ch.it.attention("Its custom body template could not be carried over: it uses Liquid that is not understood here.", "Write the body again under Alerts & notifications › Channels (body=).")
	default:
		ch.it.attention("The message body differs from Uptime Kuma's.", "Check that the receiving side understands it (Alerts & notifications › Channels › Test).")
	}
	var hs map[string]any
	if h := ch.str("webhookAdditionalHeaders"); h == "" || json.Unmarshal([]byte(h), &hs) != nil {
		return true
	}
	keys := make([]string, 0, len(hs))
	for hk := range hs {
		keys = append(keys, hk)
	}
	sort.Strings(keys)
	for _, hk := range keys {
		key := "header." + strings.ToLower(hk)
		switch {
		case !optKeyOK(key):
		case isCredentialHeader(hk):
			ch.secret(key, fmt.Sprint(hs[hk]))
		default:
			ch.plain(key, fmt.Sprint(hs[hk]))
		}
	}
	return true
}

func (ch *kumaChannel) wecom() bool {
	if ch.missing("weComBotKey") {
		return false
	}
	ch.secret("url", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key="+ch.str("weComBotKey"))
	return true
}

func (ch *kumaChannel) smtp() bool {
	if ch.missing("smtpHost", "smtpTo") {
		return false
	}
	ch.plain("host", ch.str("smtpHost"))
	ch.plain("port", ch.str("smtpPort"))
	ch.plain("username", ch.str("smtpUsername"))
	ch.secret("password", ch.str("smtpPassword"))
	from := ch.str("smtpFrom")
	if from == "" {
		from = ch.str("smtpUsername")
	}
	ch.plain("from", from)
	to := ch.str("smtpTo")
	for _, x := range []string{ch.str("smtpCC"), ch.str("smtpBCC")} {
		if x != "" {
			to += "," + x
		}
	}
	ch.plain("to", strings.ReplaceAll(to, " ", ""))
	switch {
	case ch.str("smtpSecure") == "true":
		ch.plain("tls", "tls")
	case ch.str("smtpIgnoreTLSError") == "true":
		ch.it.attention("Uptime Kuma ignored certificate errors for this mail server; Unagikuma checks them.", "")
	}
	if ch.str("smtpCC") != "" || ch.str("smtpBCC") != "" {
		ch.it.attention("Copy (CC/BCC) addresses are sent to as normal recipients.", "")
	}
	if ch.str("customSubject") != "" || ch.str("customBody") != "" {
		ch.it.attention("Its custom subject and body were not carried over.", "Edit the message templates under Notices & status if needed.")
	}
	return true
}

func (ch *kumaChannel) ntfy() bool {
	if ch.missing("ntfytopic") {
		return false
	}
	if s := ch.str("ntfyserverurl"); s != "" && s != "https://ntfy.sh" {
		ch.plain("server", strings.TrimRight(s, "/"))
	}
	ch.plain("topic", ch.str("ntfytopic"))
	ch.secret("token", ch.str("ntfyaccesstoken"))
	if ch.str("ntfyusername") != "" {
		ch.it.attention("Its user name and password login was not carried over.", "Use an access token (token=).")
	}
	return true
}

func (ch *kumaChannel) gotify() bool {
	if ch.missing("gotifyserverurl", "gotifyapplicationToken") {
		return false
	}
	ch.plain("server", strings.TrimRight(ch.str("gotifyserverurl"), "/"))
	ch.secret("token", ch.str("gotifyapplicationToken"))
	return true
}

func (ch *kumaChannel) teams() bool {
	if ch.missing("webhookUrl") {
		return false
	}
	ch.secret("url", ch.str("webhookUrl"))
	ch.it.attention("Microsoft Teams channels here use a Workflows webhook.", "If this is an old Office 365 connector URL, create a Workflows webhook and replace it.")
	return true
}

func (ch *kumaChannel) matrix() bool {
	if ch.missing("homeserverUrl", "accessToken", "internalRoomId") {
		return false
	}
	ch.plain("homeserver", strings.TrimRight(ch.str("homeserverUrl"), "/"))
	ch.secret("token", ch.str("accessToken"))
	ch.plain("room", ch.str("internalRoomId"))
	return true
}

func (ch *kumaChannel) bark() bool {
	if ch.missing("barkEndpoint") {
		return false
	}
	u, err := url.Parse(ch.str("barkEndpoint"))
	if err != nil || u.Host == "" {
		ch.drop("Its Bark address could not be read.", "Add it by hand under Alerts & notifications › Channels.")
		return false
	}
	key := strings.Trim(u.Path, "/")
	u.Path = ""
	ch.plain("server", u.String())
	ch.secret("device-key", key)
	return true
}

func (ch *kumaChannel) opsgenie() bool {
	if ch.missing("opsgenieApiKey") {
		return false
	}
	ch.secret("api-key", ch.str("opsgenieApiKey"))
	if r := ch.str("opsgenieRegion"); r != "" {
		ch.plain("region", strings.ToLower(r))
	}
	return true
}

// channelTarget describes where a channel sends, without credentials.
func channelTarget(kind string, opts []config.Opt) string {
	m := optMap(opts)
	switch {
	case kind == "email":
		return "to " + m["to"]
	case kind == "telegram":
		return "chat " + m["chat"]
	case m["topic"] != "":
		return "topic " + m["topic"]
	case m["url"] != "" && !strings.HasPrefix(m["url"], "${"):
		if u, err := url.Parse(m["url"]); err == nil {
			return u.Scheme + "://" + u.Host + "/…"
		}
	}
	return ""
}

// maintenances maps Kuma maintenance windows to MAINT records.
func (k *kumaImport) maintenances(existing *config.Config) {
	var recs []config.RecordSpec
	for _, mt := range k.bk.Maintenances {
		if rec := k.maintenance(mt, existing); rec != nil {
			recs = append(recs, *rec)
		}
	}
	if len(recs) > 0 {
		k.res.sections = append(k.res.sections, section{title: "Maintenance windows (from Uptime Kuma)", recs: recs, shared: true})
	}
}

var kumaMaintLabels = map[string]string{"recurring-weekday": "weekly", "recurring-day-of-month": "monthly", "recurring-interval": "every few days",
	"cron": "cron", "single": "once", "manual": "manual"}

// maintenance converts one window; nil when it is not imported (the item
// says why).
func (k *kumaImport) maintenance(mt kumaMaint, existing *config.Config) *config.RecordSpec {
	it := Item{Kind: "maintenance", Source: mt.Title, Type: kumaMaintLabels[mt.Strategy], Status: StatusImported}
	var names []string
	for _, id := range mt.Monitors {
		if n := k.monName[id]; n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		it.Status, it.Why = StatusLeftOut, "None of its monitors was imported."
		it.Todo = "Nothing to do, or add a window by hand under Monitors › Maintenance."
		k.res.add(it)
		return nil
	}
	opts := []config.Opt{{Key: "select", Value: "name:" + strings.Join(names, ",")}}
	opts = append(opts, k.maintSchedule(mt, &it)...)
	if it.Status == StatusLeftOut {
		if it.Why == "" {
			it.Why, it.Todo = "Its schedule could not be read.", "Add the window by hand under Monitors › Maintenance."
		}
		k.res.add(it)
		return nil
	}
	if !mt.Active {
		it.attention("It was turned off in Uptime Kuma; here it is active.", "Delete it if it is no longer needed.")
	}
	if mt.Zone != "" && mt.Zone != "SAME_AS_SERVER" && mt.Zone != "UTC" {
		it.attention("Uptime Kuma used the time zone "+mt.Zone+"; here it uses the server's time zone.", "Check the start time.")
	}
	if existing != nil {
		for _, x := range existing.Maints {
			if x.Title == mt.Title {
				it.Status, it.Name, it.Why = StatusSkipped, x.Name, "A maintenance window with this title is already here."
				it.Todo = ""
			}
		}
	}
	if it.Status == StatusSkipped {
		k.res.add(it)
		return nil
	}
	name := k.names.take(mt.Title, "maintenance")
	om := optMap(opts)
	it.Name, it.Target = name, om["every"]+om["cron"]+om["at"]
	if it.Target != "" {
		it.Target += ", " + om["duration"]
	}
	k.res.add(it)
	return &config.RecordSpec{Name: name, Type: "MAINT", Target: mt.Title, Opts: opts}
}

// hhmm keeps the hours and minutes of a "15:04:05" time.
func hhmm(s string) string {
	if len(s) >= 5 {
		return s[:5]
	}
	return s
}

// maintDuration is the window's length in seconds, from its duration or
// its start and end times.
func maintDuration(mt kumaMaint) int {
	if mt.Duration > 0 || mt.StartTime == "" || mt.EndTime == "" {
		return mt.Duration
	}
	a, err1 := time.Parse("15:04", hhmm(mt.StartTime))
	b, err2 := time.Parse("15:04", hhmm(mt.EndTime))
	if err1 != nil || err2 != nil {
		return mt.Duration
	}
	d := b.Sub(a)
	if d <= 0 {
		d += 24 * time.Hour
	}
	return int(d.Seconds())
}

// maintSchedule returns the schedule options of a window, or marks the
// item left out.
func (k *kumaImport) maintSchedule(mt kumaMaint, it *Item) []config.Opt {
	dur := maintDuration(mt)
	var opts []config.Opt
	add := func(key, v string) {
		if v != "" {
			opts = append(opts, config.Opt{Key: key, Value: v})
		}
	}
	switch mt.Strategy {
	case "recurring-weekday":
		days := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat", "sun"}
		var ws []int
		_ = json.Unmarshal([]byte(mt.Weekdays), &ws)
		var list []string
		for _, d := range ws {
			if d >= 0 && d <= 7 {
				list = append(list, days[d])
			}
		}
		if len(list) == 0 || mt.StartTime == "" || dur <= 0 {
			it.Status = StatusLeftOut
			return nil
		}
		add("every", strings.Join(list, ","))
		add("start", hhmm(mt.StartTime))
		add("duration", humanDur(dur))
	case "recurring-day-of-month", "recurring-interval", "cron":
		if mt.Cron == "" || dur <= 0 {
			it.Status = StatusLeftOut
			return nil
		}
		add("cron", mt.Cron)
		add("duration", humanDur(dur))
		if mt.Strategy == "recurring-interval" && mt.IntervalDay > 1 {
			it.attention(fmt.Sprintf("Uptime Kuma repeated it every %d days; here it repeats on the same schedule every day.", mt.IntervalDay), "Adjust cron= if that matters.")
		}
	case "single":
		return k.maintOnce(mt, it)
	case "manual":
		it.Status = StatusLeftOut
		it.Why = "It is switched on and off by hand, which has no schedule to copy."
		if mt.Active {
			it.Why += " It was on when exported."
		}
		it.Todo = "Silence the monitors (Problems › Silence) or announce the work under Notices & status › Changes."
	default:
		it.Status = StatusLeftOut
	}
	return opts
}

func (k *kumaImport) maintOnce(mt kumaMaint, it *Item) []config.Opt {
	start, err := time.Parse("2006-01-02 15:04:05", mt.StartDate)
	end, err2 := time.Parse("2006-01-02 15:04:05", mt.EndDate)
	switch {
	case err != nil || err2 != nil || !end.After(start):
		it.Status = StatusLeftOut
		return nil
	case end.Before(k.o.now()):
		it.Status, it.Why, it.Todo = StatusLeftOut, "It is over (it ended "+end.Format("2006-01-02 15:04")+").", "Nothing to do."
		return nil
	}
	return []config.Opt{{Key: "at", Value: start.Format("2006-01-02 15:04")}, {Key: "duration", Value: humanDur(int(end.Sub(start).Seconds()))}}
}

// statusPages maps Kuma status pages to STATUSPAGE records.
func (k *kumaImport) statusPages(existing *config.Config) {
	var recs []config.RecordSpec
	for _, sp := range k.bk.StatusPages {
		it := Item{Kind: "status page", Source: sp.Title, Target: "/status/" + sp.Slug, Status: StatusImported}
		var names []string
		seen := map[string]bool{}
		for _, g := range sp.Groups {
			for _, id := range g.Monitors {
				if n := k.monName[id]; n != "" && !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
			}
		}
		if len(names) == 0 {
			it.Status, it.Why, it.Todo = StatusLeftOut, "None of its monitors was imported.", "Create the page again under Notices & status › Status pages."
			k.res.add(it)
			continue
		}
		name := slug(sp.Slug)
		if existing != nil && existing.HasName(name) {
			for _, p := range existing.StatusPages {
				if p.Name == name {
					it.Status, it.Name, it.Why = StatusSkipped, name, "A status page named "+name+" is already here."
				}
			}
		}
		if it.Status == StatusSkipped {
			k.res.add(it)
			continue
		}
		name = k.names.take(sp.Slug, "status")
		opts := []config.Opt{{Key: "select", Value: "name:" + strings.Join(names, ",")}}
		if len(sp.Domains) > 0 {
			opts = append(opts, config.Opt{Key: "domain", Value: sp.Domains[0]})
		}
		if len(sp.Groups) > 1 {
			it.attention("Its monitors are shown in one list; the sections of the Uptime Kuma page are not kept.", "")
		}
		if sp.Password {
			it.attention("It was protected by a password in Uptime Kuma; here it is public.", "Delete it if it should not be public.")
		}
		it.Name, it.Target = name, "/status/"+name
		recs = append(recs, config.RecordSpec{Name: name, Type: "STATUSPAGE", Target: sp.Title, Opts: opts})
		k.res.add(it)
	}
	if len(recs) > 0 {
		k.res.sections = append(k.res.sections, section{title: "Status pages (from Uptime Kuma)", recs: recs, shared: true})
	}
}

// accounts lists Uptime Kuma's accounts in the report; the caller adds
// them.
func (k *kumaImport) accounts() {
	for _, u := range k.res.Users {
		it := Item{Kind: "account", Source: u.Username, Name: u.Username, Status: StatusImported, Target: "keeps its Uptime Kuma password"}
		switch {
		case k.o.UserExists != nil && k.o.UserExists(u.Username):
			it.Status, it.Target, it.Why = StatusSkipped, "", "An account with this name is already here; its password was not changed."
		case !u.Active:
			it.Status, it.Why, it.Todo = StatusLeftOut, "It was disabled in Uptime Kuma.", "Add it under Settings › Users if needed."
		}
		k.res.add(it)
	}
}

// namer turns display names into unique record names.
type namer struct {
	existing *config.Config
	used     map[string]bool
	next     map[string]int // next suffix to try per base name
}

func newNamer(existing *config.Config) *namer {
	return &namer{existing: existing, used: map[string]bool{}, next: map[string]int{}}
}

var slugRe = regexp.MustCompile(`[^a-z0-9_.-]+`)

func slug(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(s, "-._")
}

// maxName is the longest generated record name.
const maxName = 40

// take names a record after its display name, or after fallback when the
// display name has nothing usable (e.g. it is written in Chinese). Long
// names are cut at a word boundary.
func (n *namer) take(display, fallback string) string {
	base := shorten(slug(display))
	if base == "" || !config.ValidName(base) || !hasLetter(base) {
		base = shorten(slug(fallback))
	}
	if base == "" || !config.ValidName(base) {
		base = "imported"
	}
	name := base
	for i := max(n.next[base], 2); n.used[name] || (n.existing != nil && (n.existing.HasName(name) || name == n.existing.MName)); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
		n.next[base] = i + 1
	}
	n.used[name] = true
	return name
}

func hasLetter(s string) bool {
	for _, c := range s {
		if c >= 'a' && c <= 'z' {
			return true
		}
	}
	return false
}

// shorten cuts a slug to maxName at a dash where it can.
func shorten(s string) string {
	if len(s) <= maxName {
		return s
	}
	cut := s[:maxName]
	if i := strings.LastIndexAny(cut, "-."); i >= maxName/2 {
		cut = cut[:i]
	}
	return strings.Trim(cut, "-._")
}

func optMap(opts []config.Opt) map[string]string {
	m := map[string]string{}
	for _, o := range opts {
		m[o.Key] = o.Value
	}
	return m
}

// checkKey identifies what a monitor checks, to find ones that exist.
func checkKey(typ, target string, opts map[string]string) string {
	typ = strings.ToUpper(typ)
	switch typ {
	case "HTTP":
		return "HTTP|" + strings.TrimRight(strings.ToLower(target), "/") + "|" + opts["keyword"] + "|" + opts["json-path"] + "|" + opts["method"]
	case "DNS":
		return "DNS|" + strings.ToLower(target) + "|" + strings.ToLower(opts["query"]) + "|" + strings.ToUpper(opts["qtype"])
	case "EXEC":
		return "EXEC|" + target + "|" + opts["address"]
	}
	return typ + "|" + strings.ToLower(target)
}

// existingChecks maps the checks of a configuration to monitor names.
func existingChecks(cfg *config.Config) map[string]string {
	out := map[string]string{}
	if cfg == nil {
		return out
	}
	for _, m := range cfg.Monitors {
		typ, target := m.Written, m.Target
		if m.Written != "UDP" && m.UDP != nil {
			target = m.UDP.Host // DNS, NTP, SNMP: the address as written
		}
		key := checkKey(typ, target, m.Opts)
		if m.Type == "PUSH" {
			key = "PUSH|" + strings.ToLower(m.Title)
		}
		out[key] = m.Name
	}
	return out
}

func (n *namer) release(name string) { delete(n.used, name) }

// fromConnString turns Kuma's database connection string into a service
// check: the address, user and database; the password separately.
func fromConnString(kind, conn, host string, port int) (typ, target string, extra [][2]string, password, why string) {
	typ = map[string]string{"postgres": "POSTGRES", "mysql": "MYSQL", "redis": "REDIS"}[kind]
	if conn == "" {
		if host == "" {
			return "", "", nil, "", "connection string is empty"
		}
		target = host
		if port > 0 {
			target = net.JoinHostPort(host, strconv.Itoa(port))
		}
		return typ, target, nil, "", ""
	}
	u, err := url.Parse(conn)
	if err != nil || u.Host == "" {
		return "", "", nil, "", "connection string could not be read"
	}
	target = u.Host
	if u.Port() == "" {
		target = net.JoinHostPort(u.Hostname(), map[string]string{"postgres": "5432", "mysql": "3306", "redis": "6379"}[kind])
	}
	if u.User != nil {
		if name := u.User.Username(); name != "" {
			extra = append(extra, [2]string{"user", name})
		}
		password, _ = u.User.Password()
	}
	if db := strings.Trim(u.Path, "/"); db != "" && kind != "redis" {
		extra = append(extra, [2]string{"database", db})
	}
	switch {
	case u.Scheme == "rediss":
		extra = append(extra, [2]string{"tls", "yes"})
	case kind == "postgres" && u.Query().Get("sslmode") != "" && u.Query().Get("sslmode") != "disable":
		extra = append(extra, [2]string{"ssl", "yes"})
	}
	return typ, target, extra, password, ""
}

// channelKind is a channel's kind as the importer names it
// ("webhook:ntfy" for a webhook with preset=ntfy).
func channelKind(ch *config.Channel) string {
	if ch.Kind == "webhook" && ch.Opts["preset"] != "" {
		return "webhook:" + ch.Opts["preset"]
	}
	return ch.Kind
}

// oneLine puts a JSON body on one line, as a configuration value is: line
// breaks between JSON tokens are only spacing (inside a JSON string they
// would not be valid anyway).
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\r", "")), " ")
}
