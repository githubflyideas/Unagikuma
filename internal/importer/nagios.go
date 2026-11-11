package importer

import (
	"errors"
	"fmt"
	"net"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/notify"
)

// haveSendmail tells whether this machine can mail the way Nagios does.
var haveSendmail = func() bool { return notify.FindSendmail("") != "" }

// Nagios converts Nagios (or Icinga 1, Naemon) object definitions into
// monitors.conf records: hosts become PING checks, services the matching
// check (HTTP, TCP, DNS, databases) or an EXEC check that runs the same
// plugin, contacts e-mail channels, contact groups group channels and
// time periods PERIOD records. Templates (use=, register 0) are resolved.
// data is the object configuration; several files may simply be joined.
func Nagios(data []byte, existing *config.Config, o Options) (*Result, error) {
	return NagiosInput(&NagiosFiles{Data: data, Name: o.File, Resources: o.Resources}, existing, o)
}

// NagiosInput converts configuration read by ReadNagiosFiles.
func NagiosInput(in *NagiosFiles, existing *config.Config, o Options) (*Result, error) {
	parts := in.Parts
	if len(parts) == 0 {
		parts = []NagiosPart{{Path: in.Name, Data: in.Data}}
	}
	var objs []*nagObj
	var problems []string
	for _, p := range parts {
		data := p.Data
		if !utf8.Valid(data) {
			data = []byte(strings.ToValidUTF8(string(data), "?"))
		}
		o, probs := parseNagios(string(data))
		objs = append(objs, o...)
		where := ""
		if b := filepath.Base(p.Path); p.Path != "" && b != "." {
			where = b + ", "
		}
		for _, pr := range probs {
			problems = append(problems, where+pr)
		}
	}
	n := newNagios(objs)
	if len(n.hosts) == 0 {
		if len(in.Missing) > 0 {
			return nil, fmt.Errorf("no host definitions found; these files named in the main configuration were not found: %s", strings.Join(in.Missing, ", "))
		}
		if len(problems) > 0 {
			return nil, fmt.Errorf("no host definitions could be read: %s", problems[0])
		}
		return nil, errors.New("no host definitions found: is this a Nagios object configuration? Point to nagios.cfg or the objects directory")
	}
	if o.File == "" {
		o.File = in.Name
	}
	res := &Result{Secrets: map[string]string{}, Area: o.Area, Source: "Nagios", File: o.File, When: o.now()}
	n.names, n.have, n.res, n.o, n.existing = newNamer(existing), existingChecks(existing), res, o, existing
	n.resources = in.Resources
	if n.resources == nil {
		n.resources = o.Resources
	}
	for _, pr := range problems {
		res.add(Item{Kind: "definition", Source: pr, Status: StatusLeftOut, Why: "It could not be read, so it was skipped.",
			Todo: "Fix it in the file (Nagios would refuse it too) and import again: what is already here is left alone."})
	}
	for _, m := range in.Missing {
		res.add(Item{Kind: "file", Source: m, Status: StatusLeftOut, Why: "nagios.cfg names it, but it was not found.",
			Todo: "Copy the whole Nagios configuration directory, or pass the missing files as well."})
	}
	n.convert(o.Area)
	res.finish()
	return res, nil
}

// ---- object parser ----

type nagObj struct {
	typ  string
	vals map[string]string
	line int
}

// parseNagios reads object definitions. It is lenient like Nagios:
// comments (# and ;), CRLF, tabs, "define x{" and "}" on a value line
// are all fine; only a definition that never closes is an error.
// parseNagios reads object definitions. It is lenient: a broken
// definition is skipped and described in problems ("line N: ..."), the
// rest is still read.
func parseNagios(s string) (out []*nagObj, problems []string) {
	var cur *nagObj
	for i, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if c := strings.Index(line, ";"); c >= 0 && (c == 0 || line[c-1] != '\\') {
			line = strings.TrimSpace(line[:c])
		}
		line = strings.ReplaceAll(line, `\;`, ";")
		if line == "" || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, "define") && strings.HasSuffix(line, "{") && cur != nil {
			problems = append(problems, fmt.Sprintf("line %d: define %s is not closed (no } before the next define)", cur.line, cur.typ))
			cur = nil
		}
		if cur == nil {
			rest, ok := strings.CutPrefix(line, "define")
			if !ok {
				continue // cfg_file= and other main-config lines, stray text
			}
			typ := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "{"))
			if typ == "" || !strings.HasSuffix(line, "{") || strings.ContainsAny(typ, " \t") {
				problems = append(problems, fmt.Sprintf("line %d: expected \"define TYPE {\", found %q", i+1, truncate(line, 40)))
				continue
			}
			cur = &nagObj{typ: typ, vals: map[string]string{}, line: i + 1}
			continue
		}
		closing := strings.HasSuffix(line, "}")
		line = strings.TrimSpace(strings.TrimSuffix(line, "}"))
		if line != "" {
			k, v, _ := strings.Cut(strings.Join(strings.Fields(line), " "), " ")
			cur.vals[k] = strings.TrimSpace(v)
		}
		if closing {
			out = append(out, cur)
			cur = nil
		}
	}
	if cur != nil {
		problems = append(problems, fmt.Sprintf("line %d: define %s is not closed", cur.line, cur.typ))
	}
	return out, problems
}

type nagios struct {
	templates  map[string]map[string]*nagObj // type → name → template
	resolved   map[string]map[string]string
	members    map[string][]string // hostgroup → hosts
	direct     map[string][]string // host → hostgroups it is directly in
	hosts      []*nagObj
	services   []*nagObj
	hostByName map[string]*nagObj
	groups     map[string]*nagObj // hostgroups
	commands   map[string]string
	contacts   map[string]*nagObj
	cgroups    map[string]*nagObj
	periods    map[string]*nagObj
	sgroups    []*nagObj
	deps       []*nagObj // hostdependency
	others     map[string]int
	otherOrder []string

	names     *namer
	have      map[string]string
	res       *Result
	o         Options
	existing  *config.Config
	resources map[string]string

	hostMon                             map[string]string // host_name → monitor name
	channelOf                           map[string]string // "c:NAME" or "g:NAME" → channel name
	periodRec                           map[string]string // time period → PERIOD name ("" = always)
	policyOf                            map[string]string
	policyRecs, channelRecs, periodRecs []config.RecordSpec
	plugins                             map[string]bool
	userSecrets                         map[string]string
	contactItem                         map[string]int
}

func newNagios(objs []*nagObj) *nagios {
	n := &nagios{templates: map[string]map[string]*nagObj{}, resolved: map[string]map[string]string{}, hostByName: map[string]*nagObj{}, groups: map[string]*nagObj{},
		commands: map[string]string{}, contacts: map[string]*nagObj{}, cgroups: map[string]*nagObj{}, periods: map[string]*nagObj{},
		hostMon: map[string]string{}, channelOf: map[string]string{}, periodRec: map[string]string{}, policyOf: map[string]string{},
		others: map[string]int{}, plugins: map[string]bool{}, userSecrets: map[string]string{}, contactItem: map[string]int{}}
	for _, o := range objs {
		if name := o.vals["name"]; name != "" {
			if n.templates[o.typ] == nil {
				n.templates[o.typ] = map[string]*nagObj{}
			}
			n.templates[o.typ][name] = o
		}
	}
	for _, o := range objs {
		if o.vals["register"] == "0" {
			continue
		}
		v := n.resolve(o)
		switch o.typ {
		case "host":
			if v["host_name"] != "" {
				r := &nagObj{typ: "host", vals: v, line: o.line}
				if _, dup := n.hostByName[v["host_name"]]; !dup {
					n.hosts = append(n.hosts, r)
				}
				n.hostByName[v["host_name"]] = r
			}
		case "service":
			n.services = append(n.services, &nagObj{typ: "service", vals: v, line: o.line})
		case "hostgroup":
			n.groups[v["hostgroup_name"]] = &nagObj{vals: v}
		case "command":
			n.commands[v["command_name"]] = v["command_line"]
		case "contact":
			n.contacts[v["contact_name"]] = &nagObj{vals: v}
		case "contactgroup":
			n.cgroups[v["contactgroup_name"]] = &nagObj{vals: v}
		case "timeperiod":
			n.periods[v["timeperiod_name"]] = &nagObj{vals: v}
		case "servicegroup":
			n.sgroups = append(n.sgroups, &nagObj{vals: v})
		case "hostdependency":
			n.deps = append(n.deps, &nagObj{vals: v, line: o.line})
		default:
			if n.others[o.typ] == 0 {
				n.otherOrder = append(n.otherOrder, o.typ)
			}
			n.others[o.typ]++
		}
	}
	return n
}

// resolve merges an object with its templates: its own values win, then
// the templates in the order use= lists them; "+value" appends to the
// inherited value and "null" clears it.
func (n *nagios) resolve(o *nagObj) map[string]string {
	out := map[string]string{}
	for _, t := range splitList(o.vals["use"]) {
		for k, v := range n.template(o.typ, t) {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
	}
	for k, v := range o.vals {
		switch {
		case k == "use" || k == "name" || k == "register":
		case strings.HasPrefix(v, "+"):
			if out[k] != "" {
				out[k] += "," + v[1:]
			} else {
				out[k] = v[1:]
			}
		case v == "null":
			delete(out, k)
		default:
			out[k] = v
		}
	}
	return out
}

// template returns a resolved template, once; a template that uses
// itself resolves to nothing the second time round.
func (n *nagios) template(typ, name string) map[string]string {
	key := typ + "\x00" + name
	if v, ok := n.resolved[key]; ok {
		return v
	}
	n.resolved[key] = nil
	tpl := n.templates[typ][name]
	if tpl == nil {
		return nil
	}
	v := n.resolve(tpl)
	n.resolved[key] = v
	return v
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// ---- conversion ----

// hostsOf expands a service's host_name and hostgroup_name lists ("*"
// is every host, "!x" excludes x).
func (n *nagios) hostsOf(v map[string]string) []string {
	in, out := map[string]bool{}, map[string]bool{}
	for _, h := range splitList(v["host_name"]) {
		switch {
		case h == "*":
			for name := range n.hostByName {
				in[name] = true
			}
		case strings.HasPrefix(h, "!"):
			out[h[1:]] = true
		default:
			in[h] = true
		}
	}
	for _, g := range splitList(v["hostgroup_name"]) {
		neg := strings.HasPrefix(g, "!")
		for _, h := range n.groupMembers(strings.TrimPrefix(g, "!")) {
			if neg {
				out[h] = true
			} else {
				in[h] = true
			}
		}
	}
	var hosts []string
	for h := range in {
		if !out[h] {
			hosts = append(hosts, h)
		}
	}
	sort.Strings(hosts)
	return hosts
}

// groupMembers returns a host group's hosts, including nested groups
// and hosts naming the group in hostgroups=.
func (n *nagios) groupMembers(group string) []string {
	if n.members == nil {
		n.members, n.direct = map[string][]string{}, map[string][]string{}
		direct := map[string][]string{}
		for _, h := range n.hosts {
			for _, hg := range splitList(h.vals["hostgroups"]) {
				direct[hg] = append(direct[hg], h.vals["host_name"])
				n.direct[h.vals["host_name"]] = append(n.direct[h.vals["host_name"]], hg)
			}
		}
		for name, gr := range n.groups {
			for _, h := range splitList(gr.vals["members"]) {
				n.direct[h] = append(n.direct[h], name)
			}
		}
		for name := range n.groups {
			seen, hosts := map[string]bool{}, map[string]bool{}
			var walk func(string)
			walk = func(g string) {
				if seen[g] {
					return
				}
				seen[g] = true
				for _, h := range direct[g] {
					hosts[h] = true
				}
				if gr := n.groups[g]; gr != nil {
					for _, h := range splitList(gr.vals["members"]) {
						if h == "*" {
							for name := range n.hostByName {
								hosts[name] = true
							}
							continue
						}
						hosts[h] = true
					}
					for _, sub := range splitList(gr.vals["hostgroup_members"]) {
						walk(sub)
					}
				}
			}
			walk(name)
			list := make([]string, 0, len(hosts))
			for h := range hosts {
				list = append(list, h)
			}
			sort.Strings(list)
			n.members[name] = list
		}
	}
	return n.members[group]
}

// hostGroup is the host group a host is shown in: one it is directly in
// (the first by name), else the smallest group containing it.
func (n *nagios) hostGroup(host string) string {
	n.groupMembers("")
	if d := n.direct[host]; len(d) > 0 {
		s := append([]string(nil), d...)
		sort.Strings(s)
		for _, g := range s {
			if n.groups[g] != nil {
				return g
			}
		}
	}
	best, size := "", 0
	for name, hosts := range n.members {
		for _, h := range hosts {
			if h == host && (best == "" || len(hosts) < size || len(hosts) == size && name < best) {
				best, size = name, len(hosts)
			}
		}
	}
	return best
}

// serviceGroupsOf maps "host\x00description" to its service groups.
func (n *nagios) serviceGroupsOf() map[string][]string {
	out := map[string][]string{}
	for _, sg := range n.sgroups {
		name := sg.vals["servicegroup_name"]
		m := splitList(sg.vals["members"])
		for i := 0; i+1 < len(m); i += 2 {
			key := m[i] + "\x00" + m[i+1]
			out[key] = append(out[key], name)
		}
	}
	return out
}

// maxImported bounds one import (services times hosts can be large).
const maxImported = 20000

type nagMon struct {
	spec   *config.RecordSpec
	policy string
	host   string
	item   int
	isHost bool
}

func (n *nagios) convert(area string) {
	c := &nagConvert{n: n, byHost: map[string][]*nagMon{}}
	hosts := append([]*nagObj(nil), n.hosts...)
	sort.SliceStable(hosts, func(i, j int) bool { return hosts[i].vals["host_name"] < hosts[j].vals["host_name"] })
	// Hosts first: services name them as parents.
	for _, h := range hosts {
		c.host(h)
	}
	c.parents()
	c.services()
	// Shared records, then one section per host.
	sortSection(n.periodRecs)
	sortSection(n.channelRecs)
	sortSection(n.policyRecs)
	n.res.sections = append(n.res.sections,
		section{title: "Time periods (from Nagios timeperiods)", recs: n.periodRecs, shared: true},
		section{title: "Notification channels (from Nagios contacts and contact groups)", recs: n.channelRecs, shared: true},
		section{title: "Notification policies: who each monitor notifies, and how often", recs: n.policyRecs, shared: true})
	for _, h := range hosts {
		c.hostSection(h, area)
	}
	n.leftOvers()
}

// nagConvert carries the monitors of one Nagios conversion, by host.
type nagConvert struct {
	n      *nagios
	mons   []*nagMon
	byHost map[string][]*nagMon
}

func (c *nagConvert) keep(m *nagMon) {
	c.mons = append(c.mons, m)
	c.byHost[m.host] = append(c.byHost[m.host], m)
}

func (c *nagConvert) host(h *nagObj) {
	n, res, v := c.n, c.n.res, h.vals
	it := Item{Kind: "monitor", Source: v["host_name"], Group: n.hostGroup(v["host_name"]), Status: StatusImported}
	spec := n.hostSpec(v, &it)
	res.add(it)
	if spec == nil {
		return
	}
	c.keep(&nagMon{spec: spec, policy: n.policy(v, "host", len(res.Items)-1), host: v["host_name"], item: len(res.Items) - 1, isHost: true})
}

// parents sets parent= from the hosts' parents and host dependencies.
func (c *nagConvert) parents() {
	n, res := c.n, c.n.res
	extraParents := map[string][]string{}
	for _, d := range n.deps {
		for _, dep := range splitList(d.vals["dependent_host_name"]) {
			extraParents[dep] = append(extraParents[dep], splitList(d.vals["host_name"])...)
		}
		if d.vals["dependent_hostgroup_name"] != "" || d.vals["hostgroup_name"] != "" {
			res.add(Item{Kind: "host dependency", Source: "line " + strconv.Itoa(d.line), Status: StatusLeftOut,
				Why: "Dependencies between host groups were not converted.", Todo: "Set parent= on the dependent monitors."})
		}
	}
	for _, m := range c.mons {
		h := n.hostByName[m.host]
		var parents []string
		seen := map[string]bool{}
		for _, p := range append(splitList(h.vals["parents"]), extraParents[m.host]...) {
			pm := n.hostMon[p]
			switch {
			case pm == "":
				res.Items[m.item].attention("Its parent "+p+" is not defined.", "")
			case pm != m.spec.Name && !seen[pm]:
				seen[pm] = true
				parents = append(parents, pm)
			}
		}
		if len(parents) > 0 {
			m.spec.Opts = append(m.spec.Opts, config.Opt{Key: "parent", Value: strings.Join(parents, ",")})
		}
	}
}

func (c *nagConvert) services() {
	n, res := c.n, c.n.res
	sgOf := n.serviceGroupsOf()
	limited := false
	for _, s := range n.services {
		hosts := n.hostsOf(s.vals)
		if len(hosts) == 0 {
			res.add(Item{Kind: "service", Source: s.vals["service_description"], Status: StatusLeftOut,
				Why:  "It is not attached to any host that is defined (" + strings.TrimSpace(s.vals["host_name"]+" "+s.vals["hostgroup_name"]) + ").",
				Todo: "Nothing to do if that host is gone; otherwise add the check by hand."})
			continue
		}
		for _, host := range hosts {
			if len(c.mons) >= maxImported && n.hostByName[host] != nil {
				limited = true
				break
			}
			c.service(s, host, sgOf)
		}
	}
	if limited {
		res.add(Item{Kind: "service", Source: "the rest", Status: StatusLeftOut, Why: fmt.Sprintf("Only the first %d checks are imported at once.", maxImported),
			Todo: "Import the configuration in parts (one directory at a time)."})
	}
}

// service imports one service of one host.
func (c *nagConvert) service(s *nagObj, host string, sgOf map[string][]string) {
	n, res := c.n, c.n.res
	desc := s.vals["service_description"]
	h := n.hostByName[host]
	if h == nil {
		res.add(Item{Kind: "monitor", Source: host + " / " + desc, Status: StatusLeftOut,
			Why: "Host " + host + " is not defined.", Todo: "Nothing to do if that host is gone; otherwise add the check by hand."})
		return
	}
	v := map[string]string{}
	for k, x := range s.vals {
		v[k] = x
	}
	// Services without contacts inherit the host's.
	if v["contacts"] == "" && v["contact_groups"] == "" {
		v["contacts"], v["contact_groups"] = h.vals["contacts"], h.vals["contact_groups"]
	}
	it := Item{Kind: "monitor", Source: host + " / " + desc, Group: n.hostGroup(host), Status: StatusImported}
	spec := n.serviceSpec(v, h.vals, &it)
	if spec == nil {
		res.add(it)
		return
	}
	var tags []string
	seen := map[string]bool{}
	for _, g := range append(splitList(v["servicegroups"]), sgOf[host+"\x00"+desc]...) {
		if t := slug(g); t != "" && !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	if len(tags) > 0 {
		sort.Strings(tags)
		spec.Opts = append(spec.Opts, config.Opt{Key: "tags", Value: strings.Join(tags, ",")})
	}
	res.add(it)
	c.keep(&nagMon{spec: spec, policy: n.policy(v, "service", len(res.Items)-1), host: host, item: len(res.Items) - 1})
}

// hostSection writes the section of one host: the host check first, then
// its services by name.
func (c *nagConvert) hostSection(h *nagObj, area string) {
	n, res := c.n, c.n.res
	host := h.vals["host_name"]
	ms := c.byHost[host]
	if len(ms) == 0 {
		return
	}
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].isHost != ms[j].isHost {
			return ms[i].isHost
		}
		return ms[i].spec.Name < ms[j].spec.Name
	})
	title := "Host " + host
	if a := h.vals["alias"]; a != "" && a != host {
		title += " (" + a + ")"
	}
	if addr := h.vals["address"]; addr != "" && addr != host {
		title += ", " + addr
	}
	if g := n.hostGroup(host); g != "" {
		title += ", group " + g
	}
	sec := section{title: title}
	for _, m := range ms {
		if m.policy != "" {
			m.spec.Opts = append(m.spec.Opts, config.Opt{Key: "policy", Value: m.policy})
		}
		m.spec.Area = area
		sec.recs = append(sec.recs, *m.spec)
		res.Monitors++
	}
	res.sections = append(res.sections, sec)
}

// leftOvers reports what the import does not convert.
func (n *nagios) leftOvers() {
	res := n.res
	// Contacts and time periods nothing imported uses.
	var unusedContacts, unusedPeriods []string
	for name := range n.contacts {
		if _, ok := n.channelOf["c:"+name]; !ok {
			unusedContacts = append(unusedContacts, name)
		}
	}
	for name := range n.periods {
		if _, ok := n.periodRec[name]; !ok && name != "24x7" {
			unusedPeriods = append(unusedPeriods, name)
		}
	}
	sort.Strings(unusedContacts)
	sort.Strings(unusedPeriods)
	switch len(unusedContacts) {
	case 0:
	case 1:
		res.add(Item{Kind: "contact", Source: unusedContacts[0], Status: StatusLeftOut,
			Why: "No imported host or service notifies it.", Todo: "Add it under Alerting › Channels if you still need it."})
	default:
		res.add(Item{Kind: "contacts", Source: strings.Join(unusedContacts, ", "), Status: StatusLeftOut,
			Why: "No imported host or service notifies them.", Todo: "Add them under Alerting › Channels if you still need them."})
	}
	switch len(unusedPeriods) {
	case 0:
	case 1:
		res.add(Item{Kind: "time period", Source: unusedPeriods[0], Status: StatusLeftOut,
			Why: "No imported contact uses it.", Todo: "Nothing to do, or add a PERIOD record when a channel needs one."})
	default:
		res.add(Item{Kind: "time periods", Source: strings.Join(unusedPeriods, ", "), Status: StatusLeftOut,
			Why: "No imported contact uses them.", Todo: "Nothing to do, or add a PERIOD record when a channel needs one."})
	}
	todo := map[string][2]string{
		"serviceescalation": {"Escalations were not converted.", "Add escalate= to the policy, for example escalate=30m:manager (Alerting › Policies)."},
		"hostescalation":    {"Escalations were not converted.", "Add escalate= to the policy, for example escalate=30m:manager (Alerting › Policies)."},
		"servicedependency": {"Dependencies between services were not converted (services already depend on their host).", "Set parent= on the dependent monitor if you need more."},
		"hostextinfo":       {"Icons and extra host information are not used here.", "Nothing to do."},
		"serviceextinfo":    {"Icons and extra service information are not used here.", "Nothing to do."},
	}
	for _, t := range n.otherOrder {
		why, ok := todo[t]
		if !ok {
			why = [2]string{"This kind of definition is not used here.", "Nothing to do in most cases."}
		}
		res.add(Item{Kind: "definitions", Source: fmt.Sprintf("%d × define %s", n.others[t], t), Status: StatusLeftOut, Why: why[0], Todo: why[1]})
	}
	if len(n.plugins) > 0 {
		var ps []string
		for p := range n.plugins {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		res.add(Item{Kind: "script checks", Source: strings.Join(ps, ", "), Status: StatusAttention,
			Why:  "These checks run Nagios plugins, so they are imported paused: Unagikuma only runs plugins from its check-scripts directory.",
			Todo: "Copy the plugins into <data>/check-scripts (for example from /usr/lib/nagios/plugins), then resume the checks in the web interface."})
	}
	res.Info = append(res.Info,
		"Hosts become ping checks (or their own check command); their services depend on them, so a host that is down does not page for each service.",
		"Flap detection, freshness checks, performance data and acknowledgement settings have no direct equivalent and were not copied.",
		"Past states and history stay in Nagios: history starts fresh here.")
	if n.res.count("channel", StatusImported, StatusAttention) > 0 {
		res.Info = append(res.Info, "E-mail channels send through the local mail server (localhost:25) like Nagios usually does; change host= and from= under Alerting › Channels if needed.")
	}
}

// common sets what hosts and services share: interval, attempts, title,
// group, the event handler and disabled checks.
func (n *nagios) common(spec *config.RecordSpec, v, host map[string]string, display string, it *Item) {
	add := func(k, val string) {
		if val != "" {
			spec.Opts = append(spec.Opts, config.Opt{Key: k, Value: val})
		}
	}
	unit := 60.0
	if f, err := strconv.ParseFloat(v["interval_length"], 64); err == nil && f > 0 {
		unit = f
	}
	minutes := func(k string) string {
		f, err := strconv.ParseFloat(v[k], 64)
		if err != nil || f <= 0 {
			return ""
		}
		return humanDur(max(int(f*unit), 10))
	}
	if iv := minutes("check_interval"); iv != "" {
		spec.Interval = iv
	} else if iv := minutes("normal_check_interval"); iv != "" {
		spec.Interval = iv
	}
	if r := minutes("retry_interval"); r != "" && r != spec.Interval {
		add("retry", r)
	} else if r := minutes("retry_check_interval"); r != "" && r != spec.Interval {
		add("retry", r)
	}
	if a, err := strconv.Atoi(v["max_check_attempts"]); err == nil && a > 0 {
		add("attempts", strconv.Itoa(min(a, 100)))
	}
	if spec.Name != display {
		add("title", truncate(display, 64))
	}
	add("device", host["host_name"])
	if g := n.hostGroup(host["host_name"]); g != "" {
		add("group", g)
	}
	add("description", strings.TrimSpace(v["notes"]+" "+v["notes_url"]))
	if v["active_checks_enabled"] == "0" {
		add("disabled", "yes")
		it.attention("Its active checks were turned off in Nagios, so it is imported paused.", "Resume it when you want it checked.")
	}
	if h := v["event_handler"]; h != "" && v["event_handler_enabled"] != "0" {
		if cmd, why := n.command(h, host, v, true); cmd != "" {
			add("handler", cmd)
			it.attention("Its event handler runs "+strings.Fields(cmd)[0]+".", "Copy that script into the check-scripts directory.")
		} else {
			it.attention("Its event handler "+h+" was left out: "+why+".", "")
		}
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (n *nagios) hostSpec(v map[string]string, it *Item) *config.RecordSpec {
	host := v["host_name"]
	addr := v["address"]
	if addr == "" {
		addr = host
		it.attention("It has no address in Nagios; its name is checked instead.", "")
	}
	spec := &config.RecordSpec{Type: "PING", Target: addr}
	it.Type, it.Target = "Ping", addr
	if cmd := v["check_command"]; cmd != "" && !isPingCommand(cmd, n.commands) {
		typ, target, opts, why := n.check(cmd, v, v, it)
		if typ == "" || typ == "EXEC" {
			if why == "" {
				why = "a plugin"
			}
			it.attention("Its host check "+strings.SplitN(cmd, "!", 2)[0]+" was replaced by a ping ("+why+").", "")
		} else {
			spec.Type, spec.Target, spec.Opts = typ, target, opts
			it.Type, it.Target = typeLabel(typ), target
		}
	}
	key := checkKey(spec.Type, spec.Target, optMap(spec.Opts))
	if prev, dup := n.have[key]; dup {
		n.hostMon[host] = prev // services still depend on it
		n.duplicate(it, prev)
		return nil
	}
	spec.Name = n.names.take(host, "host")
	it.Name = spec.Name
	n.hostMon[host] = spec.Name
	n.have[key] = spec.Name
	display := host
	if a := v["alias"]; a != "" && a != host {
		display = a
	}
	n.common(spec, v, v, display, it)
	return spec
}

// duplicate marks an item whose check exists already: in the
// configuration (skipped) or earlier in this import (merged).
func (n *nagios) duplicate(it *Item, prev string) {
	it.Name = prev
	if n.existing != nil && n.existing.HasName(prev) {
		it.Status, it.Why = StatusSkipped, "The same check is already here as "+prev+"."
		return
	}
	it.Status, it.Why = StatusMerged, "It checks the same thing as "+prev+", so it was imported once, as "+prev+"."
}

func isPingCommand(cmd string, commands map[string]string) bool {
	name, _, _ := strings.Cut(cmd, "!")
	if strings.Contains(name, "-alive") || strings.Contains(name, "ping") {
		return true
	}
	words := strings.Fields(commands[name])
	return len(words) > 0 && (path.Base(words[0]) == "check_ping" || path.Base(words[0]) == "check_icmp" || path.Base(words[0]) == "check_fping")
}

func (n *nagios) serviceSpec(v, host map[string]string, it *Item) *config.RecordSpec {
	desc := v["service_description"]
	if desc == "" {
		it.Why, it.Todo = "It has no service_description.", "Add the check by hand."
		it.Status = StatusLeftOut
		return nil
	}
	if v["active_checks_enabled"] == "0" && v["passive_checks_enabled"] != "0" {
		return n.passiveSpec(v, host, it)
	}
	typ, target, opts, why := n.check(v["check_command"], host, v, it)
	if typ == "" {
		it.Status, it.Why = StatusLeftOut, why
		if strings.Contains(why, "is not defined") && len(n.resources) == 0 && it.Todo == "" {
			it.Todo = "Import nagios.cfg instead, so the command files it names are read too; or add the check by hand."
		}
		if it.Todo == "" {
			it.Todo = "Add the check by hand."
		}
		return nil
	}
	it.Type, it.Target = typeLabel(typ), target
	if typ == "EXEC" {
		it.Type = "Script (paused)"
	}
	spec := &config.RecordSpec{Type: typ, Target: target, Opts: opts}
	key := checkKey(typ, target, optMap(opts))
	if prev, dup := n.have[key]; dup {
		n.duplicate(it, prev)
		return nil
	}
	spec.Name = n.names.take(host["host_name"]+"-"+desc, "service")
	it.Name = spec.Name
	n.have[key] = spec.Name
	n.common(spec, v, host, host["host_name"]+" / "+desc, it)
	if typ == "EXEC" {
		// Plugins run only once they are in check-scripts.
		n.plugins[strings.Fields(target)[0]] = true
		if optMap(spec.Opts)["disabled"] == "" {
			spec.Opts = append(spec.Opts, config.Opt{Key: "disabled", Value: "yes"})
		}
	}
	if p := n.hostMon[host["host_name"]]; p != "" && p != spec.Name {
		// A service is unreachable, not failed, while its host is down.
		spec.Opts = append(spec.Opts, config.Opt{Key: "parent", Value: p})
	}
	return spec
}

// passiveSpec turns a passive-only service into a PUSH monitor: results
// are pushed to its URL instead of sent through NSCA or NRDP.
func (n *nagios) passiveSpec(v, host map[string]string, it *Item) *config.RecordSpec {
	desc := v["service_description"]
	spec := &config.RecordSpec{Type: "PUSH"}
	period := 0
	if f, err := strconv.Atoi(v["freshness_threshold"]); err == nil && f > 0 {
		period = f
	} else if f, err := strconv.ParseFloat(v["check_interval"], 64); err == nil && f > 0 {
		period = int(f * 60 * 2)
	}
	period = max(period, 3600)
	spec.Opts = append(spec.Opts, config.Opt{Key: "period", Value: humanDur(period)})
	key := "PUSH|" + strings.ToLower(host["host_name"]+" / "+desc)
	if prev, dup := n.have[key]; dup {
		n.duplicate(it, prev)
		return nil
	}
	spec.Name = n.names.take(host["host_name"]+"-"+desc, "passive")
	it.Name, it.Type, it.Target = spec.Name, "Push", "waits for results"
	n.have[key] = spec.Name
	v2 := map[string]string{}
	for k, x := range v {
		if k != "active_checks_enabled" && k != "check_interval" && k != "retry_interval" && k != "max_check_attempts" {
			v2[k] = x
		}
	}
	n.common(spec, v2, host, host["host_name"]+" / "+desc, it)
	it.attention("It only received passive results in Nagios; here it waits for results sent to its push URL ("+humanDur(period)+" without one is a failure).",
		"Have the sender call the push URL shown on the monitor (…/api/push/TOKEN?status=up|down&msg=…) instead of NSCA or NRDP.")
	return spec
}

var macroRe = regexp.MustCompile(`\$[A-Za-z0-9_]+\$`)

// command resolves "name!arg1!arg2" into the plugin's command line with
// $ARGn$, $USERn$, the host macros and custom variables ($_HOSTx$,
// $_SERVICEx$) substituted. $SERVICEDESC$ and $TIMEOUT$ are kept: they
// are filled in when the check runs. For event handlers the state macros
// are kept too.
func (n *nagios) command(call string, host, svc map[string]string, handler bool) (string, string) {
	parts := strings.Split(call, "!")
	line, ok := n.commands[parts[0]]
	if !ok {
		return "", "the command " + parts[0] + " is not defined"
	}
	x := &macros{n: n, parts: parts, host: host, svc: svc, handler: handler, addr: host["address"]}
	if x.addr == "" {
		x.addr = host["host_name"]
	}
	line = x.expand(line, 0)
	if len(x.unknown) > 0 {
		whys := make([]string, 0, len(x.unknown))
		for _, u := range x.unknown {
			whys = append(whys, n.unknownMacro(u, host))
		}
		return "", strings.Join(whys, "; ")
	}
	words := shellWords(line)
	if len(words) == 0 {
		return "", "its command line is empty"
	}
	words[0] = path.Base(words[0])
	for i, w := range words {
		if i > 0 && (w == "" || strings.ContainsAny(w, " \t'\"\\")) {
			words[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
	}
	return strings.Join(words, " "), ""
}

// macros fills in the macros of one command call.
type macros struct {
	n         *nagios
	parts     []string
	host, svc map[string]string
	addr      string
	handler   bool
	unknown   []string
}

func (x *macros) expand(s string, depth int) string {
	return macroRe.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := x.macro(m, depth); ok {
			return v
		}
		x.unknown = append(x.unknown, m)
		return m
	})
}

var handlerStates = map[string]bool{"SERVICESTATE": true, "SERVICESTATETYPE": true, "SERVICEATTEMPT": true,
	"HOSTSTATE": true, "HOSTSTATETYPE": true, "HOSTATTEMPT": true}

// macro is the value of the macro m ("$NAME$"); ok is false when it
// cannot be filled in.
func (x *macros) macro(m string, depth int) (string, bool) {
	name := m[1 : len(m)-1]
	switch {
	case strings.HasPrefix(name, "ARG"):
		i, err := strconv.Atoi(name[3:])
		switch {
		case err != nil || i >= len(x.parts):
			return "", true
		case depth == 0:
			return x.expand(x.parts[i], 1), true
		}
		return x.parts[i], true
	case strings.HasPrefix(name, "USER"):
		return x.user(name)
	case name == "HOSTADDRESS":
		return x.addr, true
	case name == "HOSTNAME":
		return x.host["host_name"], true
	case name == "HOSTALIAS":
		return x.host["alias"], true
	case name == "SERVICEDESC" || name == "TIMEOUT":
		return m, true
	case strings.HasPrefix(name, "_HOST"):
		return customVar(x.host, name[5:])
	case strings.HasPrefix(name, "_SERVICE"):
		return customVar(x.svc, name[8:])
	case x.handler && handlerStates[name]:
		return "$" + strings.Replace(name, "HOST", "SERVICE", 1) + "$", true
	}
	return "", false
}

// user resolves $USERn$: a plugin directory is dropped (plugins are looked
// up by name), any other value becomes a secret.
func (x *macros) user(name string) (string, bool) {
	n := x.n
	v, ok := n.resources[name]
	switch {
	case name == "USER1" || ok && strings.HasPrefix(v, "/"):
		return "", true
	case !ok:
		return "", false
	}
	sn, have := n.userSecrets[name]
	if !have {
		sn = "nagios_" + strings.ToLower(name)
		for i := 2; n.o.SecretExists != nil && n.o.SecretExists(sn); i++ {
			sn = "nagios_" + strings.ToLower(name) + "_" + strconv.Itoa(i)
		}
		n.userSecrets[name] = sn
		n.res.Secrets[sn] = v
	}
	return "${secret:" + sn + "}", true
}

func customVar(vals map[string]string, name string) (string, bool) {
	for k, v := range vals {
		if strings.HasPrefix(k, "_") && strings.EqualFold(k[1:], name) {
			return v, true
		}
	}
	return "", false
}

// unknownMacro says why the macro u could not be filled in.
func (n *nagios) unknownMacro(u string, host map[string]string) string {
	name := u[1 : len(u)-1]
	switch {
	case strings.HasPrefix(name, "USER") && len(n.resources) == 0:
		return "it uses " + u + ", and no resource file (resource.cfg) with the $USERn$ values was read: import nagios.cfg instead of single files"
	case strings.HasPrefix(name, "USER"):
		return "it uses " + u + ", which is not set in the resource file"
	case strings.HasPrefix(name, "_HOST"):
		return "it uses " + u + ", but host " + host["host_name"] + " has no _" + name[5:] + " custom variable"
	case strings.HasPrefix(name, "_SERVICE"):
		return "it uses " + u + ", but the service has no _" + name[8:] + " custom variable"
	}
	return "it uses " + u + ", which cannot be filled in here"
}

// shellWords splits a command line the way Nagios's shell would, without
// expanding anything.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, q := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0 && c == q:
			q = 0
		case q == '\'':
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s) && q != '\'':
			i++
			cur.WriteByte(s[i])
			in = true
		case q == 0 && (c == '\'' || c == '"'):
			q, in = c, true
		case q == 0 && (c == ' ' || c == '\t'):
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// flags reads a plugin's short and long options ("-H x", "--port=80").
func flags(words []string, withValue string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") {
			continue
		}
		k, v, hasEq := strings.Cut(strings.TrimLeft(w, "-"), "=")
		if !strings.HasPrefix(w, "--") && len(k) > 1 && strings.Contains(withValue, " "+k[:1]+" ") {
			k, v, hasEq = k[:1], k[1:], true // -H10.0.0.1
		}
		if !hasEq && strings.Contains(withValue, " "+k+" ") && i+1 < len(words) {
			i++
			v = words[i]
		}
		if _, dup := out[k]; dup && k == "k" {
			out[k] += "\n" + v // several headers
			continue
		}
		out[k] = v
	}
	return out
}

func pick(f map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := f[k]; ok {
			return v
		}
	}
	return ""
}

var plainPorts = map[string]string{"check_ssh": "22", "check_smtp": "25", "check_ftp": "21", "check_imap": "143", "check_pop": "110",
	"check_simap": "993", "check_spop": "995", "check_ssmtp": "465", "check_ldap": "389", "check_ldaps": "636", "check_jabber": "5222",
	"check_nntp": "119", "check_clamd": "3310", "check_telnet": "23"}

// check maps a check command to a monitor type, target and options.
func (n *nagios) check(call string, host, svc map[string]string, it *Item) (typ, target string, opts []config.Opt, why string) {
	if call == "" {
		return "", "", nil, "It has no check_command."
	}
	line, why := n.command(call, host, svc, false)
	if line == "" {
		return "", "", nil, capitalize(why) + "."
	}
	c := newNagiosCheck(n, line, host, svc, it)
	if fn, ok := map[string]func() (string, string, string, bool){
		"check_ping": c.ping, "check_icmp": c.ping, "check_fping": c.ping,
		"check_http": c.http, "check_curl": c.http,
		"check_tcp": c.tcp, "check_udp": c.udp,
		"check_dns": c.dns, "check_dig": c.dns,
		"check_ntp": c.ntp, "check_ntp_time": c.ntp, "check_ntp_peer": c.ntp, "check_time": c.ntp,
		"check_snmp": c.snmp, "check_mysql": c.mysql, "check_pgsql": c.pgsql, "check_dummy": c.dummy,
	}[c.plugin]; ok {
		if typ, target, why, handled := fn(); handled {
			if why != "" {
				return "", "", nil, why
			}
			return typ, target, c.opts, ""
		}
	}
	if port, ok := plainPorts[c.plugin]; ok {
		if p := pick(c.f, "p", "port"); p != "" {
			port = p
		}
		return "TCP", net.JoinHostPort(c.h, port), nil, ""
	}
	// Anything else runs the same plugin.
	c.add("address", c.addr)
	return "EXEC", line, c.opts, ""
}

// nagiosCheck turns one Nagios plugin call into a check here.
type nagiosCheck struct {
	n         *nagios
	plugin    string
	args      []string
	f         map[string]string
	addr, h   string
	host, svc map[string]string
	it        *Item
	opts      []config.Opt
}

func newNagiosCheck(n *nagios, line string, host, svc map[string]string, it *Item) *nagiosCheck {
	raw := shellWords(line)
	c := &nagiosCheck{n: n, plugin: raw[0], host: host, svc: svc, it: it}
	// Options passed together in one quoted $ARGn$ ('-S -u /x') are read
	// one by one, the way they were meant.
	for _, w := range raw[1:] {
		if strings.HasPrefix(w, "-") && strings.ContainsAny(w, " \t") {
			c.args = append(c.args, shellWords(w)...)
		} else if w != "" {
			c.args = append(c.args, w)
		}
	}
	c.addr = host["address"]
	if c.addr == "" {
		c.addr = host["host_name"]
	}
	const valued = " H I u p s e j w c t C a A k P l d D 4 6 r R T b f m M N o O "
	c.f = flags(c.args, valued+"hostname IPaddress url uri port string expect method warning critical timeout certificate expect-address query-address server database logname username user password community oid header regex ")
	c.h = pick(c.f, "I", "IPaddress", "H", "hostname")
	if c.h == "" {
		c.h = c.addr
		for _, a := range c.args { // check_ssh HOST: the host as the last word
			if !strings.HasPrefix(a, "-") && a == c.addr {
				c.h = a
			}
		}
	}
	return c
}

func (c *nagiosCheck) add(k, v string) {
	if v != "" {
		c.opts = append(c.opts, config.Opt{Key: k, Value: v})
	}
}

// secret stores value as a secret named after the service and refers to it.
func (c *nagiosCheck) secret(key, kind, value string) {
	if value == "" {
		return
	}
	sn := c.n.secretName(c.n.names.peek(c.host["host_name"]+"-"+c.svc["service_description"]), kind)
	c.n.res.Secrets[sn] = value
	c.add(key, "${secret:"+sn+"}")
}

func (c *nagiosCheck) ping() (string, string, string, bool) { return "PING", c.h, "", true }

func (c *nagiosCheck) ntp() (string, string, string, bool) { return "NTP", c.h, "", true }

func (c *nagiosCheck) dummy() (string, string, string, bool) {
	return "", "", "check_dummy always returns the same state; there is nothing to check.", true
}

func (c *nagiosCheck) tcp() (string, string, string, bool) {
	port := pick(c.f, "p", "port")
	if port == "" {
		return "", "", "check_tcp has no port (-p).", true
	}
	return "TCP", net.JoinHostPort(c.h, port), "", true
}

func (c *nagiosCheck) udp() (string, string, string, bool) {
	port := pick(c.f, "p", "port")
	if port == "" {
		return "", "", "check_udp has no port (-p).", true
	}
	c.it.attention("check_udp sent a fixed string; this UDP check speaks the protocol of the port when it knows it.", "")
	return "UDP", net.JoinHostPort(c.h, port), "", true
}

func (c *nagiosCheck) http() (string, string, string, bool) {
	f := c.f
	vhost := pick(f, "H", "hostname")
	ip := pick(f, "I", "IPaddress")
	hostHeader := ""
	switch {
	case vhost == "":
		vhost = c.h
	case ip != "" && ip != vhost && !strings.Contains(vhost, "."):
		// -H web1 -I 10.0.0.5: connect to the address, send the name.
		hostHeader, vhost = vhost, ip
	}
	c.add("host", hostHeader)
	c.add("keyword", pick(f, "s", "string"))
	if r := pick(f, "r", "regex", "ereg"); r != "" {
		c.add("keyword-regex", r)
	}
	if _, ok := f["invert-regex"]; ok {
		c.add("absent", "yes")
	}
	if e := pick(f, "e", "expect"); e != "" {
		c.add("expect", strings.Join(nagiosStatusCodes(e), ","))
	}
	if m := pick(f, "j", "method"); m != "" && !strings.EqualFold(m, "GET") {
		c.add("method", strings.ToUpper(m))
	}
	c.add("body", pick(f, "P", "post"))
	if cert := pick(f, "C", "certificate"); cert != "" {
		days, crit, _ := strings.Cut(cert, ",")
		c.add("cert-days", days)
		c.add("cert-critical-days", crit)
	}
	c.secret("basic", "basic", pick(f, "a", "authorization"))
	for _, hv := range strings.Split(pick(f, "k", "header"), "\n") {
		if k, v, ok := strings.Cut(hv, ":"); ok && optKeyOK("header."+strings.ToLower(strings.TrimSpace(k))) {
			c.add("header."+strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v))
		}
	}
	follow, ok := f["f"]
	switch {
	case !ok:
		c.add("no-follow", "yes")
		if e := optMap(c.opts)["expect"]; e == "" {
			c.add("expect", "200-399")
		}
	case follow != "follow" && follow != "sticky" && follow != "stickyport":
		c.add("no-follow", "yes")
	}
	return "HTTP", nagiosURL(f, vhost), "", true
}

// nagiosURL is the address check_http asks for.
func nagiosURL(f map[string]string, vhost string) string {
	uri := pick(f, "u", "url", "uri")
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return uri
	}
	scheme, port := "http", pick(f, "p", "port")
	if _, ok := f["S"]; ok {
		scheme = "https"
	}
	if _, ok := f["ssl"]; ok {
		scheme = "https"
	}
	hostport := vhost
	if port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		hostport = net.JoinHostPort(vhost, port)
	} else if strings.Contains(vhost, ":") {
		hostport = "[" + vhost + "]"
	}
	if !strings.HasPrefix(uri, "/") {
		uri = "/" + uri
	}
	return scheme + "://" + hostport + uri
}

// nagiosStatusCodes reads check_http's -e ("HTTP/1.1 200,302") as codes.
func nagiosStatusCodes(e string) []string {
	var codes []string
	for _, x := range strings.Split(e, ",") {
		c := strings.TrimSpace(x)
		if after, ok := strings.CutPrefix(c, "HTTP/1.1 "); ok {
			c = after
		} else if after, ok := strings.CutPrefix(c, "HTTP/1.0 "); ok {
			c = after
		}
		if len(c) >= 3 && c[0] >= '1' && c[0] <= '5' {
			if _, err := strconv.Atoi(c[:3]); err == nil {
				codes = append(codes, c[:3])
			}
		}
	}
	return codes
}

func (c *nagiosCheck) dns() (string, string, string, bool) {
	query := pick(c.f, "H", "hostname", "l", "lookup")
	server := pick(c.f, "s", "server")
	if c.plugin == "check_dig" {
		server = pick(c.f, "H", "hostname")
		query = pick(c.f, "l", "lookup")
	}
	if server == "" {
		return "", "", "check_dns without -s asks the system's resolver, and this DNS check needs a server to ask.", true
	}
	if query == "" {
		return "", "", "It names no record to look up.", true
	}
	c.add("query", query)
	c.add("expect", strings.ReplaceAll(pick(c.f, "a", "expect-address"), " ", ""))
	if t := pick(c.f, "q", "querytype", "T", "record_type"); t != "" && !strings.EqualFold(t, "A") {
		c.add("qtype", strings.ToUpper(t))
	}
	return "DNS", server, "", true
}

// snmp imports a plain "does it answer" check; one with thresholds, a
// match or another OID runs the plugin itself (handled is false).
func (c *nagiosCheck) snmp() (string, string, string, bool) {
	oid := pick(c.f, "o", "oid")
	for _, k := range []string{"w", "c", "s", "r"} {
		if _, ok := c.f[k]; ok {
			return "", "", "", false
		}
	}
	if oid != "" && !strings.HasPrefix(oid, "sysUpTime") && !strings.HasPrefix(oid, ".1.3.6.1.2.1.1.3") && !strings.HasPrefix(oid, "1.3.6.1.2.1.1.3") {
		return "", "", "", false
	}
	c.secret("community", "community", pick(c.f, "C", "community"))
	return "SNMP", c.h, "", true
}

func (c *nagiosCheck) mysql() (string, string, string, bool) {
	return c.database("MYSQL", "3306", pick(c.f, "u", "username", "user"))
}

func (c *nagiosCheck) pgsql() (string, string, string, bool) {
	return c.database("POSTGRES", "5432", pick(c.f, "l", "logname"))
}

func (c *nagiosCheck) database(typ, defPort, user string) (string, string, string, bool) {
	port := pick(c.f, "P", "port")
	if port == "" {
		port = defPort
	}
	c.add("user", user)
	c.add("database", pick(c.f, "d", "database"))
	c.secret("password", "password", pick(c.f, "p", "password"))
	return typ, net.JoinHostPort(c.h, port), "", true
}

// secretName picks an unused secret name for a record's credential.
func (n *nagios) secretName(rec, key string) string {
	base := "nagios_" + strings.NewReplacer("-", "_", ".", "_").Replace(rec) + "_" + key
	name := base
	for i := 2; n.res.Secrets[name] != "" || (n.o.SecretExists != nil && n.o.SecretExists(name)); i++ {
		name = base + "_" + strconv.Itoa(i)
	}
	return name
}

// peek is the name take would give, without taking it.
func (n *namer) peek(display string) string {
	base := shorten(slug(display))
	if base == "" {
		return "check"
	}
	return base
}

// ---- notification: contacts, groups, periods, policies ----

// policy returns the policy for an object's contacts and notification
// settings, creating channels, groups, periods and the policy as needed.
func (n *nagios) policy(v map[string]string, kind string, item int) string {
	chain := n.contactChain(v, kind)
	if len(chain) == 0 {
		return ""
	}
	if v["notifications_enabled"] == "0" {
		n.res.Items[item].attention("Notifications were turned off for it in Nagios, so it has no policy.", "")
		return ""
	}
	opts := []config.Opt{{Key: "chain", Value: strings.Join(chain, ",")}, {Key: "mode", Value: "all"}}
	timing, repeat := nagiosTiming(v)
	opts = append(opts, timing...)
	var key strings.Builder
	for _, o := range opts {
		key.WriteString(o.Key + "=" + o.Value + " ")
	}
	if p, ok := n.policyOf[key.String()]; ok {
		return p
	}
	if n.existing != nil {
		for _, p := range n.existing.Policies {
			if strings.Join(p.Chain, ",") == strings.Join(chain, ",") && strings.HasPrefix(p.Name, "notify-") && n.sameRecord(p.Name, opts) {
				n.policyOf[key.String()] = p.Name
				return p.Name
			}
		}
	}
	p := n.names.take(n.policyBase(chain, repeat), "notify")
	n.policyOf[key.String()] = p
	n.policyRecs = append(n.policyRecs, config.RecordSpec{Name: p, Type: "POLICY", Opts: opts})
	what := "sends to " + strings.Join(chain, ", ")
	if repeat != "" && repeat != "once" {
		what += ", repeats every " + repeat
	}
	n.res.add(Item{Kind: "policy", Source: p, Name: p, Target: what, Status: StatusImported})
	return p
}

// contactChain is the sorted channels of a host's or service's contacts
// and contact groups.
func (n *nagios) contactChain(v map[string]string, kind string) []string {
	var chain []string
	seen := map[string]bool{}
	add := func(ch string) {
		if ch != "" && !seen[ch] {
			seen[ch] = true
			chain = append(chain, ch)
		}
	}
	for _, c := range splitList(v["contacts"]) {
		add(n.contactChannel(c, kind))
	}
	for _, g := range splitList(v["contact_groups"]) {
		add(n.groupChannel(g, kind))
	}
	sort.Strings(chain)
	return chain
}

// nagiosTiming turns Nagios's notification timing into policy options;
// repeat is "once", the repeat interval, or "".
func nagiosTiming(v map[string]string) (opts []config.Opt, repeat string) {
	unit := 60.0
	if f, err := strconv.ParseFloat(v["interval_length"], 64); err == nil && f > 0 {
		unit = f
	}
	if f, err := strconv.ParseFloat(v["notification_interval"], 64); err == nil {
		if f == 0 {
			opts = append(opts, config.Opt{Key: "max", Value: "1"})
			repeat = "once"
		} else if s := int(f * unit); s >= 60 {
			repeat = humanDur(s)
			opts = append(opts, config.Opt{Key: "repeat", Value: repeat})
		}
	}
	if f, err := strconv.ParseFloat(v["first_notification_delay"], 64); err == nil && f > 0 {
		opts = append(opts, config.Opt{Key: "delay", Value: humanDur(int(f * unit))})
	}
	if no := v["notification_options"]; no != "" && !strings.Contains(no, "r") && no != "n" {
		opts = append(opts, config.Opt{Key: "recovery", Value: "no"})
	}
	return opts, repeat
}

// policyBase names a policy after its channels; policies differing only in
// timing are told apart by it.
func (n *nagios) policyBase(chain []string, repeat string) string {
	base := "notify-" + strings.Join(chain, "-")
	if len(base) > 40 {
		base = "notify-" + chain[0] + "-and-" + strconv.Itoa(len(chain)-1) + "-more"
	}
	for _, other := range n.policyOf {
		if strings.HasPrefix(other, base) && repeat != "" {
			return base + "-" + repeat
		}
	}
	return base
}

// sameRecord reports whether an existing record has these options.
func (n *nagios) sameRecord(name string, opts []config.Opt) bool {
	p := n.existing.FindPolicy(name)
	if p == nil {
		return false
	}
	m := optMap(opts)
	return p.Mode == m["mode"]
}

func (n *nagios) contactChannel(name, kind string) string {
	if ch, ok := n.channelOf["c:"+name]; ok {
		return ch
	}
	n.channelOf["c:"+name] = ""
	c := n.contacts[name]
	if c == nil {
		n.res.add(Item{Kind: "contact", Source: name, Status: StatusLeftOut, Why: "It is named by a host or service, but not defined.", Todo: "Nothing to do, or add a channel for this person."})
		return ""
	}
	v := c.vals
	if v[kind+"_notifications_enabled"] == "0" {
		return ""
	}
	if v["email"] == "" {
		it := Item{Kind: "contact", Source: name, Status: StatusLeftOut, Why: "It has no e-mail address."}
		if v["pager"] != "" {
			it.Why = "It has only a pager number."
		}
		it.Todo = "Add a channel for this person (SMS, chat or e-mail) under Alerting › Channels."
		n.res.add(it)
		return ""
	}
	if n.existing != nil {
		if ch := n.existing.FindChannel(slug(name)); ch != nil && ch.Kind == "email" {
			n.channelOf["c:"+name] = ch.Name
			n.res.add(Item{Kind: "channel", Source: name, Name: ch.Name, Type: "Email", Status: StatusSkipped, Why: "An e-mail channel with this name is already here; it is used."})
			return ch.Name
		}
	}
	ch := n.names.take(name, "contact")
	// Nagios mails through the machine's own mail system (its notify
	// commands pipe into mail or sendmail), so the channel does the same
	// when this machine has a sendmail command: whatever mail system is
	// behind it, postfix, sendmail, exim or msmtp, keeps relaying as before.
	// Without one, the nearest thing is that mail system's SMTP port.
	var opts []config.Opt
	if haveSendmail() {
		opts = []config.Opt{{Key: "via", Value: "sendmail"}, {Key: "to", Value: v["email"]}}
	} else {
		opts = []config.Opt{{Key: "host", Value: "localhost"}, {Key: "port", Value: "25"}, {Key: "tls", Value: "none"},
			{Key: "from", Value: "unagikuma@localhost"}, {Key: "to", Value: v["email"]}}
	}
	it := Item{Kind: "channel", Source: name, Name: ch, Type: "Email", Target: "to " + v["email"], Status: StatusImported}
	if len(opts) > 2 {
		it.attention("This machine has no sendmail command, so the channel sends to the SMTP server on localhost:25.",
			"Run the import on the Nagios machine, or set the SMTP server under Alerting › Channels.")
	}
	if p := n.period(v["service_notification_period"]); p != "" {
		opts = append(opts, config.Opt{Key: "period", Value: p})
		it.Target += ", during " + p
	}
	if v["pager"] != "" {
		it.attention("The pager number ("+v["pager"]+") was not carried over.", "Add an SMS or voice channel for it.")
	}
	n.channelRecs = append(n.channelRecs, config.RecordSpec{Name: ch, Type: "CHANNEL", Target: "email", Opts: opts})
	n.res.Channels++
	n.res.add(it)
	n.channelOf["c:"+name] = ch
	return ch
}

func (n *nagios) groupChannel(name, kind string) string {
	if ch, ok := n.channelOf["g:"+name]; ok {
		return ch
	}
	n.channelOf["g:"+name] = ""
	g := n.cgroups[name]
	if g == nil {
		n.res.add(Item{Kind: "contact group", Source: name, Status: StatusLeftOut, Why: "It is named by a host or service, but not defined.", Todo: "Nothing to do."})
		return ""
	}
	var members []string
	seen := map[string]bool{}
	visited := map[string]bool{}
	var walk func(string)
	walk = func(group string) {
		cg := n.cgroups[group]
		if cg == nil || visited[group] {
			return
		}
		visited[group] = true
		var names []string
		names = append(names, splitList(cg.vals["members"])...)
		// Contacts naming the group in their contactgroups= are members too.
		for cname, c := range n.contacts {
			for _, x := range splitList(c.vals["contactgroups"]) {
				if x == group {
					names = append(names, cname)
				}
			}
		}
		sort.Strings(names[len(splitList(cg.vals["members"])):])
		for _, c := range names {
			if ch := n.contactChannel(c, kind); ch != "" && !seen[ch] {
				seen[ch] = true
				members = append(members, ch)
			}
		}
		for _, sub := range splitList(cg.vals["contactgroup_members"]) {
			walk(sub)
		}
	}
	walk(name)
	if len(members) == 0 {
		n.res.add(Item{Kind: "contact group", Source: name, Status: StatusLeftOut, Why: "None of its members could be imported (see the contacts above).", Todo: "Add channels for its members."})
		return ""
	}
	if len(members) == 1 {
		n.channelOf["g:"+name] = members[0]
		return members[0]
	}
	ch := n.names.take(name, "contacts")
	n.channelRecs = append(n.channelRecs, config.RecordSpec{Name: ch, Type: "CHANNEL", Target: "group",
		Opts: []config.Opt{{Key: "members", Value: strings.Join(members, ",")}}})
	n.res.Channels++
	n.res.add(Item{Kind: "contact group", Source: name, Name: ch, Type: "group", Target: "notifies " + strings.Join(members, ", "), Status: StatusImported})
	n.channelOf["g:"+name] = ch
	return ch
}

var nagDays = []struct{ long, short string }{{"monday", "mon"}, {"tuesday", "tue"}, {"wednesday", "wed"}, {"thursday", "thu"},
	{"friday", "fri"}, {"saturday", "sat"}, {"sunday", "sun"}}

// period converts a time period into a PERIOD record and returns its name
// ("" when it is always on or cannot be converted).
func (n *nagios) period(name string) string {
	if name == "" {
		return ""
	}
	if p, ok := n.periodRec[name]; ok {
		return p
	}
	n.periodRec[name] = ""
	tp := n.periods[name]
	if tp == nil {
		n.res.add(Item{Kind: "time period", Source: name, Status: StatusLeftOut, Why: "It is used but not defined; the channel is used at all times.", Todo: "Nothing to do, or set period= on the channel."})
		return ""
	}
	// Equal weekly ranges are written once: "mon-fri 09:00-17:00".
	var rules []string
	always := true
	var dayRanges []string
	for _, d := range nagDays {
		r := strings.Join(splitList(tp.vals[d.long]), ",")
		dayRanges = append(dayRanges, r)
		if r != "00:00-24:00" {
			always = false
		}
	}
	for i := 0; i < len(nagDays); {
		j := i
		for j+1 < len(nagDays) && dayRanges[j+1] == dayRanges[i] {
			j++
		}
		if dayRanges[i] != "" {
			days := nagDays[i].short
			if j > i {
				days += "-" + nagDays[j].short
			}
			for _, r := range splitList(dayRanges[i]) {
				rules = append(rules, days+" "+r)
			}
		}
		i = j + 1
	}
	it := Item{Kind: "time period", Source: name, Status: StatusImported}
	var dropped []string
	for k := range tp.vals {
		known := k == "timeperiod_name" || k == "alias"
		for _, d := range nagDays {
			known = known || k == d.long
		}
		if !known {
			dropped = append(dropped, k)
		}
	}
	sort.Strings(dropped)
	if len(dropped) > 0 {
		it.attention("Dates and exclusions ("+strings.Join(dropped, ", ")+") were not carried over; only the weekly hours were.", "Add holidays with $HOLIDAYS if needed.")
	}
	if always {
		return ""
	}
	if len(rules) == 0 {
		it.Status, it.Why, it.Todo = StatusLeftOut, "It has no weekly hours; the channel is used at all times.", "Set period= on the channel if needed."
		n.res.add(it)
		return ""
	}
	rec := n.names.take(name, "period")
	it.Name, it.Target = rec, strings.Join(rules, "; ")
	n.periodRecs = append(n.periodRecs, config.RecordSpec{Name: rec, Type: "PERIOD", Target: strings.Join(rules, "; ")})
	n.res.add(it)
	n.periodRec[name] = rec
	return rec
}
