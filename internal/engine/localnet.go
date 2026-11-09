package engine

import (
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Local network anomaly detection: the single-machine defence
// against "my own uplink/router died, now everything is red".
//
// Inputs are observations (every probe result on this node) and anchor
// results (auto gateway, explicit lan/wan anchors). The detector judges per
// address family:
//
//   - gateway down: every usable lan anchor (the auto gateway once it has
//     answered, explicit lan anchors) failed AnchorFails times in a row and
//     no routed (not on-link) target of that family answered within Window;
//   - uplink down: since the episode started (first network-class wan
//     failure), at least MinDomains independent wan failure domains that were
//     not already failing before the episode failed with network-class
//     reasons and no wan target answered at the network layer (any reply
//     vetoes) — or every explicit wan anchor is down;
//   - local DNS broken: DNSMinNames names fail resolution while the network
//     itself demonstrably works and no name resolved within Window.
//
// Everything is explicit-time and side-effect free; the engine feeds results,
// calls Evaluate and asks Affects() how to treat each vote.

// Family is an IP address family.
type Family int

const (
	FamilyV4 Family = 4
	FamilyV6 Family = 6
)

// FamilyOf returns the family of an address (v4 for v4-mapped v6).
func FamilyOf(a netip.Addr) Family {
	if a.Unmap().Is4() {
		return FamilyV4
	}
	return FamilyV6
}

// Anomaly kinds.
const (
	AnomalyGateway = "gateway_down" // router unreachable: routed lan and all wan affected
	AnomalyNoRoute = "no_route"     // no default route of the family (boot, link down)
	AnomalyUplink  = "uplink_down"  // router fine, internet unreachable: wan affected
	AnomalyDNS     = "dns_down"     // this host's resolver broken: name-addressed targets affected
)

// LocalNetConfig holds the tunables.
type LocalNetConfig struct {
	Enabled        bool
	MinDomains     int
	DomainTTL      time.Duration
	DNSMinNames    int
	Window         time.Duration
	ExitRatio      float64
	ExitWindows    int
	AnchorFails    int
	AnchorRecovers int
	AnchorEnable   time.Duration
	// Backdate: a target that answered this recently before an episode is
	// not "already dead" — it is the episode's first victim.
	Backdate time.Duration
	// MaxHold: a local outage that lasts longer than this stops holding
	// the monitors back (fail open), in case the judgement is wrong. 0 = never.
	MaxHold time.Duration
	// References: internet reference points (IP addresses in different
	// networks) the pre-HARD self-check asks besides the monitored targets.
	References []string
}

// LocalNetConfigFrom reads the tunables.
func LocalNetConfigFrom(t tunables.Set) LocalNetConfig {
	return LocalNetConfig{
		Enabled:        t.Bool("localnet.enabled"),
		MinDomains:     t.Int("localnet.min-domains"),
		DomainTTL:      t.Duration("localnet.domain-ttl"),
		DNSMinNames:    t.Int("localnet.dns-min-names"),
		Window:         t.Duration("localnet.window"),
		Backdate:       t.Duration("localnet.backdate"),
		MaxHold:        t.Duration("localnet.max-hold"),
		ExitRatio:      t.Float("localnet.exit-ratio"),
		ExitWindows:    t.Int("localnet.exit-windows"),
		AnchorFails:    t.Int("localnet.anchor-fails"),
		AnchorRecovers: t.Int("localnet.anchor-recovers"),
		AnchorEnable:   t.Duration("localnet.anchor-enable"),
		References:     referenceList(t.Raw("localnet.references")),
	}
}

// referenceList parses localnet.references, skipping what is not an address.
func referenceList(v string) []string {
	var out []string
	for _, a := range strings.Split(v, ",") {
		if ip, err := netip.ParseAddr(strings.TrimSpace(a)); err == nil {
			out = append(out, ip.Unmap().String())
		}
	}
	return out
}

// Observation is one probe result as seen by the detector.
type Observation struct {
	At     time.Time
	Scope  netscope.Scope
	Family Family // 0 when the name did not resolve
	OnLink bool   // target is in a directly connected prefix (not via the gateway)
	Domain string // failure domain (FailureDomain)
	ByName bool   // target addressed by DNS name
	// NetworkFail: network-class failure (probe.NetworkClass).
	NetworkFail bool
	// Reachable: the remote side answered at the network layer
	// (probe.RemoteReachable); vetoes local outage judgements.
	Reachable bool
	// DNSFail: the resolver failed for the target name (reason dns_resolve).
	DNSFail bool
	// Recheck is a cheap IP-level target (e.g. "tcp:203.0.113.5:443") the
	// pre-HARD gate may re-probe.
	Recheck string
	// Reference: the result of asking an internet reference point
	// (localnet.references), not of a monitored target.
	Reference bool
}

// AnchorKind distinguishes anchors.
type AnchorKind string

const (
	AnchorAutoGateway AnchorKind = "auto-gateway"
	AnchorLAN         AnchorKind = "lan"
	AnchorWAN         AnchorKind = "wan"
)

// AnchorResult is one anchor check.
type AnchorResult struct {
	At     time.Time
	Kind   AnchorKind
	Family Family
	Addr   string
	OK     bool
	RTT    time.Duration
	// Container marks an auto gateway that is a container bridge
	// (docker0/CNI): never used for judgements.
	Container bool
	// NoRoute marks "there is no default route of this family".
	NoRoute bool
}

// Anomaly is an active local network anomaly.
type Anomaly struct {
	Kind   string    `json:"kind"`
	Family Family    `json:"family"` // 0 for DNS
	Since  time.Time `json:"since"`
	// Suspected marks an uplink judgement made from target correlation
	// alone (no wan anchors configured): a large upstream outage (a CDN
	// down) looks the same, and the UI says so and suggests wan anchors.
	Suspected bool   `json:"suspected,omitzero"`
	Domains   int    `json:"domains"`          // failing wan domains (uplink) or names (dns) at entry
	Detail    string `json:"detail,omitempty"` // gateway address, …
}

// LocalNetEvent is an enter/exit transition.
type LocalNetEvent struct {
	Enter   bool
	Anomaly Anomaly
	At      time.Time
	Back    time.Time // an exit: when the evidence first said the network was back
}

type anchorState struct {
	kind      AnchorKind
	addr      string
	answered  bool // ever answered (enables the auto gateway)
	container bool
	noRoute   bool
	fails     int
	oks       int
	down      bool
	rtt       time.Duration
}

type domainState struct {
	lastAt        time.Time
	reachable     bool // last result proved reachability
	networkFail   bool // last result was a network-class failure
	resolver      bool // last result was the resolver failing for the name (no address known)
	lastReachable time.Time
	recheck       string
	reference     bool // only ever seen as an internet reference point
}

type famState struct {
	domains         map[string]*domainState // wan failure domains
	episodeStart    time.Time               // first network-class wan failure of the current episode
	preDead         map[string]bool         // domains already failing when the episode started
	lastReachWAN    time.Time               // last wan network-layer success (incl. wan anchors)
	lastReachRouted time.Time               // last success of a target behind the gateway
	uplink          *Anomaly
	gateway         *Anomaly
	healthySince    time.Time // uplink exit condition continuously true since
	autoGateway     *anchorState
	anchorsLAN      map[string]*anchorState
	anchorsWAN      map[string]*anchorState
}

// LocalNet is the detector. Not safe for concurrent use; the engine serialises.
type LocalNet struct {
	cfg     LocalNetConfig
	started time.Time
	fam     map[Family]*famState
	dns     *Anomaly
	dnsFail map[string]time.Time // name failure domain → last resolver failure
	dnsOK   time.Time            // last successful resolution
	// released: the running outage outlasted MaxHold; votes are no longer
	// excluded until every anomaly has ended.
	released bool
}

// NewLocalNet creates a detector; start is the process start time.
func NewLocalNet(cfg LocalNetConfig, start time.Time) *LocalNet {
	return &LocalNet{cfg: cfg, started: start, fam: map[Family]*famState{}, dnsFail: map[string]time.Time{}}
}

// SetConfig replaces tunables (hot reload).
func (d *LocalNet) SetConfig(cfg LocalNetConfig) { d.cfg = cfg }

func (d *LocalNet) f(fam Family) *famState {
	s := d.fam[fam]
	if s == nil {
		s = &famState{domains: map[string]*domainState{}, anchorsLAN: map[string]*anchorState{}, anchorsWAN: map[string]*anchorState{}}
		d.fam[fam] = s
	}
	return s
}

// Observe records one probe result.
func (d *LocalNet) Observe(o Observation) {
	if o.ByName {
		if o.DNSFail {
			d.dnsFail[o.Domain] = o.At
		} else {
			// The name resolved, whatever happened next.
			delete(d.dnsFail, o.Domain)
			if o.At.After(d.dnsOK) {
				d.dnsOK = o.At
			}
		}
	}
	if o.Family == 0 {
		// The name did not resolve, so the family is unknown. When the
		// resolver could not even be reached (a network-class failure),
		// that is as much evidence of a dead uplink as a timeout — the
		// usual picture of a host started without network — so it counts
		// for every family. Otherwise each such monitor would alert on
		// its own.
		if !o.NetworkFail || o.Scope != netscope.WAN || o.Domain == "" {
			return
		}
		// Only the families this host really reaches the internet over:
		// a family known only from its gateway (IPv6 on a v4 network) would
		// otherwise see nothing but these failures and call its uplink dead.
		var fams []Family
		for _, f := range d.families() {
			for _, ds := range d.fam[f].domains {
				if !ds.resolver {
					fams = append(fams, f)
					break
				}
			}
		}
		if len(fams) == 0 {
			fams = d.families()
		}
		if len(fams) == 0 {
			fams = []Family{FamilyV4}
		}
		for _, f := range fams {
			oo := o
			oo.Family, oo.ByName = f, false // the resolver failure is already recorded
			d.Observe(oo)
		}
		return
	}
	s := d.f(o.Family)
	if o.Reachable && !o.OnLink && o.At.After(s.lastReachRouted) {
		s.lastReachRouted = o.At
	}
	if o.Scope != netscope.WAN || o.Domain == "" {
		return
	}
	ds := s.domains[o.Domain]
	if ds == nil {
		ds = &domainState{}
		s.domains[o.Domain] = ds
	}
	seenBefore, wasReachable, lastOK := !ds.lastAt.IsZero(), ds.reachable, ds.lastReachable
	ds.reference = o.Reference && (!seenBefore || ds.reference)
	ds.lastAt = o.At
	ds.reachable = o.Reachable
	ds.networkFail = o.NetworkFail
	ds.resolver = o.DNSFail
	if o.Recheck != "" {
		ds.recheck = o.Recheck
	}
	switch {
	case o.Reachable:
		ds.lastReachable = o.At
		if o.At.After(s.lastReachWAN) {
			s.lastReachWAN = o.At
		}
		if s.uplink == nil {
			// Any reply vetoes the current episode.
			s.episodeStart, s.preDead = time.Time{}, nil
		}
	case o.NetworkFail && s.uplink == nil && s.episodeStart.IsZero() && !o.At.Before(s.lastReachWAN):
		// (A failure from before the last reply, observed late because
		// rounds end out of order, does not start one: the next does.)
		s.episodeStart = o.At
		// Targets that were already failing are not evidence of a new
		// local outage (a host that has been dead for days stays out) —
		// unless they answered just before: a target that stopped a few
		// moments ago, while another still answered, fell to the same cut.
		s.preDead = map[string]bool{}
		dead := func(reachable bool, last time.Time) bool {
			return !reachable && (last.IsZero() || o.At.Sub(last) > d.cfg.Backdate)
		}
		for k, v := range s.domains {
			if k != o.Domain && dead(v.reachable, v.lastReachable) {
				s.preDead[k] = true
			}
		}
		if seenBefore && dead(wasReachable, lastOK) {
			s.preDead[o.Domain] = true
		}
	}
}

// Anchor records one anchor check.
func (d *LocalNet) Anchor(r AnchorResult) {
	s := d.f(r.Family)
	var a *anchorState
	switch r.Kind {
	case AnchorAutoGateway:
		if s.autoGateway == nil || s.autoGateway.addr != r.Addr {
			// New gateway (other Wi-Fi, DHCP renew): start over.
			s.autoGateway = &anchorState{kind: r.Kind, addr: r.Addr}
		}
		a = s.autoGateway
	case AnchorLAN, AnchorWAN:
		m := s.anchorsLAN
		if r.Kind == AnchorWAN {
			m = s.anchorsWAN
		}
		if a = m[r.Addr]; a == nil {
			a = &anchorState{kind: r.Kind, addr: r.Addr}
			m[r.Addr] = a
		}
	default:
		return
	}
	a.container = r.Container
	a.noRoute = r.NoRoute
	if r.OK {
		a.answered = true
		a.rtt = r.RTT
		a.fails = 0
		a.oks++
		if a.down && a.oks >= max(1, d.cfg.AnchorRecovers) {
			a.down = false
		}
	} else {
		a.oks = 0
		a.fails++
		if a.fails >= max(1, d.cfg.AnchorFails) {
			a.down = true
		}
	}
	if r.Kind == AnchorWAN && r.OK {
		if r.At.After(s.lastReachWAN) {
			s.lastReachWAN = r.At
		}
		if r.At.After(s.lastReachRouted) {
			s.lastReachRouted = r.At
		}
		if s.uplink == nil {
			s.episodeStart, s.preDead = time.Time{}, nil
		}
	}
}

// RemoveAnchor forgets an anchor (config change, gateway gone).
func (d *LocalNet) RemoveAnchor(kind AnchorKind, fam Family, addr string) {
	s := d.fam[fam]
	if s == nil {
		return
	}
	switch kind {
	case AnchorAutoGateway:
		if s.autoGateway != nil && s.autoGateway.addr == addr {
			s.autoGateway = nil
		}
	case AnchorLAN:
		delete(s.anchorsLAN, addr)
	case AnchorWAN:
		delete(s.anchorsWAN, addr)
	}
}

// lanAnchors returns the anchors that may drive "gateway down": the auto
// gateway only once it has answered and when it is not a container bridge
// (a router that ignores ping must not silence the whole instance).
func lanAnchors(s *famState) []*anchorState {
	var out []*anchorState
	if g := s.autoGateway; g != nil && g.answered && !g.container {
		out = append(out, g)
	}
	addrs := make([]string, 0, len(s.anchorsLAN))
	for a := range s.anchorsLAN {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, a := range addrs {
		out = append(out, s.anchorsLAN[a])
	}
	return out
}

// activeDomains counts the wan failure domains usable as evidence: probed
// within DomainTTL and not already failing when the episode started.
func (d *LocalNet) activeDomains(s *famState, now time.Time) int {
	n := 0
	for k, ds := range s.domains {
		if now.Sub(ds.lastAt) <= d.cfg.DomainTTL && !s.preDead[k] {
			n++
		}
	}
	return n
}

func wanAnchorsState(s *famState) (configured, anyOK, allDown bool) {
	if len(s.anchorsWAN) == 0 {
		return false, false, false
	}
	allDown = true
	for _, a := range s.anchorsWAN {
		if !a.down {
			allDown = false
		}
		if a.answered && a.fails == 0 {
			anyOK = true
		}
	}
	return true, anyOK, allDown
}

// Evaluate updates anomaly states and returns transitions.
func (d *LocalNet) Evaluate(now time.Time) []LocalNetEvent {
	if !d.cfg.Enabled {
		return d.clearAll(now)
	}
	var evs []LocalNetEvent
	for _, fam := range d.families() {
		s := d.fam[fam]
		evs = append(evs, d.evalGateway(fam, s, now)...)
		evs = append(evs, d.evalUplink(fam, s, now)...)
	}
	evs = append(evs, d.evalDNS(now)...)
	if len(d.Active()) == 0 {
		d.released = false
	}
	return evs
}

// Release reports, once, that the running local outage has lasted longer
// than MaxHold: from then on the monitors are judged normally again (fail
// open), so a wrong "it is our network" judgement can never hide a real
// incident for good. since is when the oldest active anomaly began.
func (d *LocalNet) Release(now time.Time) (since time.Time, ok bool) {
	if d.released || d.cfg.MaxHold <= 0 {
		return time.Time{}, false
	}
	act := d.Active()
	if len(act) == 0 || now.Sub(act[0].Since) < d.cfg.MaxHold {
		return time.Time{}, false
	}
	d.released = true
	return act[0].Since, true
}

// Released reports whether the running outage outlasted MaxHold.
func (d *LocalNet) Released() bool { return d.released }

func (d *LocalNet) families() []Family {
	fams := make([]Family, 0, len(d.fam))
	for f := range d.fam {
		fams = append(fams, f)
	}
	sort.Slice(fams, func(i, j int) bool { return fams[i] < fams[j] })
	return fams
}

func (d *LocalNet) evalGateway(fam Family, s *famState, now time.Time) []LocalNetEvent {
	kind, detail, down := AnomalyGateway, "", false
	if g := s.autoGateway; g != nil && g.noRoute {
		kind, down = AnomalyNoRoute, true
	} else if as := lanAnchors(s); len(as) > 0 {
		down = true
		for _, a := range as {
			if !a.down {
				down = false
			}
		}
		if down {
			detail = as[0].addr
			// Veto: traffic through the router still flows; the anchors just
			// stopped answering our checks.
			down = s.lastReachRouted.IsZero() || now.Sub(s.lastReachRouted) > d.cfg.Window
		}
	}
	switch {
	case s.gateway == nil && down:
		a := Anomaly{Kind: kind, Family: fam, Since: now, Detail: detail}
		s.gateway = &a
		return []LocalNetEvent{{Enter: true, Anomaly: a, At: now}}
	case s.gateway != nil && !down:
		ev := LocalNetEvent{Anomaly: *s.gateway, At: now}
		s.gateway = nil
		return []LocalNetEvent{ev}
	}
	return nil
}

func (d *LocalNet) evalUplink(fam Family, s *famState, now time.Time) []LocalNetEvent {
	if s.gateway != nil && s.uplink == nil {
		return nil // the router is down: that anomaly already covers wan
	}
	configured, anyOK, allDown := wanAnchorsState(s)
	if s.uplink == nil {
		enter, failing := false, 0
		switch {
		case configured && anyOK:
			// A wan anchor answers: the uplink is fine.
		case configured && allDown:
			enter = true
		case !s.episodeStart.IsZero() && !s.lastReachWAN.After(s.episodeStart):
			// A reply stamped the same instant as the first failure (rounds
			// started together) is not a reply after it.
			addrKnown, addrFailing := false, 0
			for k, ds := range s.domains {
				if !ds.resolver && !s.preDead[k] && now.Sub(ds.lastAt) <= d.cfg.DomainTTL {
					addrKnown = true
				}
				if ds.networkFail && !ds.lastAt.Before(s.episodeStart) && !s.preDead[k] {
					failing++
					if !ds.resolver {
						addrFailing++
					}
				}
			}
			// Only names failing to resolve while the targets checked by
			// address are not failing: that is the resolver (see evalDNS),
			// not the uplink. Without such targets the resolver's failures
			// are all the evidence there is.
			enter = failing >= max(2, d.cfg.MinDomains) && (!addrKnown || addrFailing > 0)
		}
		if !enter {
			return nil
		}
		since := s.episodeStart
		if since.IsZero() || (configured && allDown) {
			since = now
		}
		a := Anomaly{Kind: AnomalyUplink, Family: fam, Since: since, Suspected: !configured, Domains: failing}
		s.uplink = &a
		s.healthySince = time.Time{}
		return []LocalNetEvent{{Enter: true, Anomaly: a, At: now}}
	}
	// Exit: a wan anchor answers, or enough of the evidence domains answer
	// again, continuously for ExitWindows windows.
	recovered, total := 0, 0
	for k, ds := range s.domains {
		if s.preDead[k] || now.Sub(ds.lastAt) > d.cfg.DomainTTL {
			continue
		}
		if ds.reference && !(ds.reachable && ds.lastAt.After(s.uplink.Since)) {
			// Reference points are asked only when a target fails, so one
			// that has not been asked since is no evidence either way.
			continue
		}
		total++
		if ds.reachable && ds.lastAt.After(s.uplink.Since) {
			recovered++
		}
	}
	healthy := anyOK || (total > 0 && float64(recovered)/float64(total) >= d.cfg.ExitRatio)
	if !healthy || s.gateway != nil {
		s.healthySince = time.Time{}
		return nil
	}
	if s.healthySince.IsZero() {
		s.healthySince = now
	}
	if !anyOK && now.Sub(s.healthySince) < time.Duration(max(1, d.cfg.ExitWindows)-1)*d.cfg.Window {
		return nil
	}
	ev := LocalNetEvent{Anomaly: *s.uplink, At: now, Back: s.healthySince}
	s.uplink, s.episodeStart, s.preDead, s.healthySince = nil, time.Time{}, nil, time.Time{}
	return []LocalNetEvent{ev}
}

func (d *LocalNet) evalDNS(now time.Time) []LocalNetEvent {
	failing := 0
	var first time.Time
	for _, at := range d.dnsFail {
		if now.Sub(at) <= d.cfg.Window {
			failing++
			if first.IsZero() || at.Before(first) {
				first = at
			}
		}
	}
	// The resolver is the problem only if the network itself works: an
	// IP-level wan target or a wan anchor answered within the window.
	netOK := false
	for _, s := range d.fam {
		if s.uplink == nil && s.gateway == nil && !s.lastReachWAN.IsZero() && now.Sub(s.lastReachWAN) <= d.cfg.Window {
			netOK = true
		}
	}
	// A name resolved since the failures began: the resolver works. (A
	// name resolved just before them says nothing.)
	nameOK := !d.dnsOK.IsZero() && now.Sub(d.dnsOK) <= d.cfg.Window && (first.IsZero() || d.dnsOK.After(first))
	switch {
	case d.dns == nil && netOK && !nameOK && failing >= max(1, d.cfg.DNSMinNames):
		a := Anomaly{Kind: AnomalyDNS, Since: now, Domains: failing}
		d.dns = &a
		return []LocalNetEvent{{Enter: true, Anomaly: a, At: now}}
	case d.dns != nil && (nameOK || failing == 0):
		ev := LocalNetEvent{Anomaly: *d.dns, At: now}
		d.dns = nil
		return []LocalNetEvent{ev}
	}
	return nil
}

func (d *LocalNet) clearAll(now time.Time) []LocalNetEvent {
	var evs []LocalNetEvent
	for _, fam := range d.families() {
		s := d.fam[fam]
		for _, p := range []**Anomaly{&s.gateway, &s.uplink} {
			if *p != nil {
				evs = append(evs, LocalNetEvent{Anomaly: **p, At: now})
				*p = nil
			}
		}
		s.episodeStart, s.preDead = time.Time{}, nil
	}
	if d.dns != nil {
		evs = append(evs, LocalNetEvent{Anomaly: *d.dns, At: now})
		d.dns = nil
	}
	d.released = false
	return evs
}

// Affects tells whether a vote of a monitor must be excluded right now and
// with which abstain reason.
func (d *LocalNet) Affects(scope netscope.Scope, fam Family, onLink, byName bool) (excluded bool, reason string) {
	if !d.cfg.Enabled || d.released {
		return false, ""
	}
	fams := []Family{fam}
	if fam == 0 {
		fams = d.families() // an unresolved name: whichever family it would use
	}
	for _, f := range fams {
		if s := d.fam[f]; s != nil {
			if s.gateway != nil && !onLink {
				return true, "local_network" // the router is gone: routed lan and all wan
			}
			if s.uplink != nil && scope == netscope.WAN {
				return true, "local_network"
			}
		}
	}
	if d.dns != nil && byName {
		return true, "local_dns"
	}
	return false, ""
}

// Pending reports whether a local outage is still being assessed for a
// wan path: network failures began, nothing on the internet answered
// within the last window, and the window to decide has not passed. Failures seen meanwhile
// wait instead of alerting — at a cold start without network every target
// fails at once, before enough evidence has come in. Any reply ends it;
// a target that is really down alerts after one window at most.
func (d *LocalNet) Pending(scope netscope.Scope, fam Family, now time.Time) bool {
	if !d.cfg.Enabled || d.released || scope != netscope.WAN {
		return false
	}
	fams := []Family{fam}
	if fam == 0 {
		fams = d.families()
	}
	for _, f := range fams {
		s := d.fam[f]
		if s == nil || s.uplink != nil || s.episodeStart.IsZero() {
			continue
		}
		quiet := s.lastReachWAN.IsZero() || now.Sub(s.lastReachWAN) > d.cfg.Window
		if quiet && now.Sub(s.episodeStart) < d.cfg.Window {
			return true
		}
	}
	return false
}

// Active returns the active anomalies, oldest first.
func (d *LocalNet) Active() []Anomaly {
	var out []Anomaly
	for _, fam := range d.families() {
		s := d.fam[fam]
		if s.gateway != nil {
			out = append(out, *s.gateway)
		}
		if s.uplink != nil {
			out = append(out, *s.uplink)
		}
	}
	if d.dns != nil {
		out = append(out, *d.dns)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// GateTargets returns up to n re-check targets from distinct wan domains that
// answered within DomainTTL, most recent first, for the pre-HARD self-check
// (HARD gate). exclude is the failure domain of the failing monitor.
func (d *LocalNet) GateTargets(fam Family, exclude string, n int, now time.Time) []string {
	s := d.fam[fam]
	if s == nil || n <= 0 {
		return nil
	}
	type cand struct {
		at time.Time
		t  string
	}
	var cs []cand
	for k, ds := range s.domains {
		if k != exclude && ds.recheck != "" && !ds.lastReachable.IsZero() && now.Sub(ds.lastReachable) <= d.cfg.DomainTTL {
			cs = append(cs, cand{ds.lastReachable, ds.recheck})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if !cs[i].at.Equal(cs[j].at) {
			return cs[i].at.After(cs[j].at)
		}
		return cs[i].t < cs[j].t
	})
	out := make([]string, 0, min(n, len(cs)))
	for _, c := range cs[:min(n, len(cs))] {
		out = append(out, c.t)
	}
	return out
}

// Health is the "本机健康" card model (overview health card).
type Health struct {
	Uplink      string        `json:"uplink"` // ok | wan_down | lan_down | no_route | cannot_judge | unknown
	UplinkSince time.Time     `json:"uplink_since,omitzero"`
	Suspected   bool          `json:"suspected,omitzero"` // wan_down judged without wan anchors
	Gateway     string        `json:"gateway,omitempty"`
	GatewayOK   bool          `json:"gateway_ok"`
	GatewayRTT  time.Duration `json:"gateway_rtt,omitzero"`
	GatewayNote string        `json:"gateway_note,omitempty"` // container | unresponsive | no_route | ""
	// GatewayState: ok | down (answered before, stopped) | silent (never
	// answered ping: many routers ignore it, which is fine) | unsure.
	GatewayState   string        `json:"gateway_state,omitempty"`
	WANDomains     int           `json:"wan_domains"`
	WANAnchors     int           `json:"wan_anchors"`
	DNSDown        bool          `json:"dns_down,omitzero"`
	CanJudgeUplink bool          `json:"can_judge_uplink"`
	Exit           string        `json:"exit,omitempty"` // internet exit address, "" when off or not checked yet
	ExitOK         bool          `json:"exit_ok,omitzero"`
	ExitRTT        time.Duration `json:"exit_rtt,omitzero"`
	ExitAt         time.Time     `json:"exit_at,omitzero"`
	ExitIdle       bool          `json:"exit_idle,omitzero"` // not pinged because every internet target answers
}

// Health summarises the detector for the UI (IPv4 preferred, else IPv6).
func (d *LocalNet) Health(now time.Time) Health {
	h := Health{Uplink: "unknown", DNSDown: d.dns != nil}
	s := d.fam[FamilyV4]
	if s == nil {
		s = d.fam[FamilyV6]
	}
	if s == nil {
		return h
	}
	if g := s.autoGateway; g != nil {
		h.Gateway, h.GatewayRTT = g.addr, g.rtt
		h.GatewayOK = g.answered && !g.down && g.fails == 0
		switch {
		case h.GatewayOK:
			h.GatewayState = "ok"
		case !g.answered || g.container:
			h.GatewayState = "silent"
		case g.down:
			h.GatewayState = "down"
		default:
			h.GatewayState = "unsure"
		}
		switch {
		case g.noRoute:
			h.GatewayNote = "no_route"
		case g.container:
			h.GatewayNote = "container"
		case !g.answered && now.Sub(d.started) >= d.cfg.AnchorEnable:
			h.GatewayNote = "unresponsive"
		}
	}
	h.WANDomains = d.activeDomains(s, now)
	h.WANAnchors = len(s.anchorsWAN)
	h.CanJudgeUplink = h.WANDomains >= max(2, d.cfg.MinDomains) || h.WANAnchors > 0 || len(d.cfg.References) >= max(2, d.cfg.MinDomains)
	switch {
	case s.gateway != nil && s.gateway.Kind == AnomalyNoRoute:
		h.Uplink, h.UplinkSince = "no_route", s.gateway.Since
	case s.gateway != nil:
		h.Uplink, h.UplinkSince = "lan_down", s.gateway.Since
	case s.uplink != nil:
		h.Uplink, h.UplinkSince, h.Suspected = "wan_down", s.uplink.Since, s.uplink.Suspected
	case !h.CanJudgeUplink:
		h.Uplink = "cannot_judge"
	default:
		h.Uplink = "ok"
	}
	return h
}

// FailureDomain groups targets that fail together: IPv4 /24,
// IPv6 /48, or — when only a name is known (resolution failed) — the
// registrable domain.
func FailureDomain(host string, addr netip.Addr) string {
	if addr.IsValid() {
		addr = addr.Unmap()
		if addr.Is4() {
			p, _ := addr.Prefix(24)
			return "4:" + p.String()
		}
		p, _ := addr.Prefix(48)
		return "6:" + p.String()
	}
	return "n:" + RegistrableDomain(host)
}

// twoLevelSuffixes are common public suffixes with two labels; enough to
// group subdomains of one site without shipping the public suffix list.
var twoLevelSuffixes = map[string]bool{
	"com.cn": true, "net.cn": true, "org.cn": true, "gov.cn": true, "edu.cn": true, "ac.cn": true,
	"com.hk": true, "com.tw": true, "org.tw": true, "co.jp": true, "ne.jp": true, "or.jp": true,
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "com.au": true, "net.au": true,
	"org.au": true, "co.nz": true, "co.kr": true, "com.br": true, "com.sg": true, "com.my": true,
	"co.in": true, "co.za": true, "com.mx": true, "com.tr": true, "com.ru": true,
}

// RegistrableDomain approximates eTLD+1 ("blog.example.com" → "example.com").
func RegistrableDomain(host string) string {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	labels := strings.Split(h, ".")
	if len(labels) <= 2 {
		return h
	}
	if twoLevelSuffixes[strings.Join(labels[len(labels)-2:], ".")] {
		return strings.Join(labels[len(labels)-3:], ".")
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
