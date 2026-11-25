package engine

import (
	"net/netip"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func defaultLocalNet(t *testing.T) (*LocalNet, LocalNetConfig) {
	t.Helper()
	cfg := LocalNetConfigFrom(tunables.Set{})
	return NewLocalNet(cfg, t0), cfg
}

func wanOK(at time.Time, domain string) Observation {
	return Observation{At: at, Scope: netscope.WAN, Family: FamilyV4, Domain: domain, Reachable: true, Recheck: "tcp:" + domain}
}

func wanFail(at time.Time, domain string) Observation {
	return Observation{At: at, Scope: netscope.WAN, Family: FamilyV4, Domain: domain, NetworkFail: true}
}

func sec(n int) time.Duration { return time.Duration(n) * time.Second }

func enters(evs []LocalNetEvent, kind string) bool {
	for _, e := range evs {
		if e.Enter && e.Anomaly.Kind == kind {
			return true
		}
	}
	return false
}

func exits(evs []LocalNetEvent, kind string) bool {
	for _, e := range evs {
		if !e.Enter && e.Anomaly.Kind == kind {
			return true
		}
	}
	return false
}

func TestLocalNetConfigDefaults(t *testing.T) {
	_, cfg := defaultLocalNet(t)
	if !cfg.Enabled || cfg.MinDomains != 3 || cfg.Window != time.Minute || cfg.AnchorFails != 3 || cfg.AnchorRecovers != 2 || cfg.DNSMinNames != 2 || cfg.DomainTTL != 10*time.Minute {
		t.Fatalf("defaults: %+v", cfg)
	}
}

func TestUplinkDownAndExit(t *testing.T) {
	d, _ := defaultLocalNet(t)
	doms := []string{"4:1.1.1.0/24", "4:8.8.8.0/24", "4:9.9.9.0/24", "4:203.0.113.0/24"}
	for _, dm := range doms {
		d.Observe(wanOK(t0, dm))
	}
	if evs := d.Evaluate(t0); len(evs) != 0 {
		t.Fatalf("healthy: %+v", evs)
	}
	// Two domains fail: below min-domains, nothing happens.
	d.Observe(wanFail(t0.Add(sec(10)), doms[0]))
	d.Observe(wanFail(t0.Add(sec(11)), doms[1]))
	if evs := d.Evaluate(t0.Add(sec(12))); len(evs) != 0 {
		t.Fatalf("two domains must not trigger: %+v", evs)
	}
	d.Observe(wanFail(t0.Add(sec(13)), doms[2]))
	evs := d.Evaluate(t0.Add(sec(14)))
	if !enters(evs, AnomalyUplink) {
		t.Fatalf("three domains must trigger: %+v", evs)
	}
	if a := d.Active(); len(a) != 1 || !a[0].Since.Equal(t0.Add(sec(10))) || a[0].Domains != 3 {
		t.Fatalf("anomaly since episode start: %+v", a)
	}
	if ex, r := d.Affects(netscope.WAN, FamilyV4, false, false); !ex || r != "local_network" {
		t.Fatalf("wan must be excluded: %v %q", ex, r)
	}
	if ex, _ := d.Affects(netscope.LAN, FamilyV4, true, false); ex {
		t.Fatal("lan must not be excluded by an uplink outage")
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV6, false, false); ex {
		t.Fatal("other family must not be affected")
	}
	if h := d.Health(t0.Add(sec(14))); h.Uplink != "wan_down" || !h.Suspected {
		t.Fatalf("health: %+v", h)
	}

	// Recovery: 2 of 4 domains answer (ratio 0.5) → must hold for
	// exit-windows (2) windows before exiting.
	now := t0.Add(5 * time.Minute)
	d.Observe(wanOK(now, doms[0]))
	d.Observe(wanOK(now, doms[1]))
	if evs := d.Evaluate(now); len(evs) != 0 {
		t.Fatalf("exit too early: %+v", evs)
	}
	if evs := d.Evaluate(now.Add(30 * time.Second)); len(evs) != 0 {
		t.Fatalf("exit before the second window: %+v", evs)
	}
	evs = d.Evaluate(now.Add(time.Minute))
	if !exits(evs, AnomalyUplink) {
		t.Fatalf("expected exit: %+v", evs)
	}
	// The summary says when the network was back, not when that was confirmed.
	if !evs[0].Back.Equal(now) {
		t.Fatalf("back at %v, want %v", evs[0].Back, now)
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV4, false, false); ex {
		t.Fatal("still excluded after exit")
	}
}

func TestUplinkExitResetsWhenRelapsing(t *testing.T) {
	d, _ := defaultLocalNet(t)
	doms := []string{"4:a", "4:b", "4:c"}
	for _, dm := range doms {
		d.Observe(wanOK(t0, dm))
	}
	for i, dm := range doms {
		d.Observe(wanFail(t0.Add(sec(10+i)), dm))
	}
	if !enters(d.Evaluate(t0.Add(sec(20))), AnomalyUplink) {
		t.Fatal("enter")
	}
	now := t0.Add(2 * time.Minute)
	d.Observe(wanOK(now, doms[0]))
	d.Observe(wanOK(now, doms[1]))
	d.Evaluate(now)
	// Relapse inside the hold period.
	d.Observe(wanFail(now.Add(sec(20)), doms[0]))
	d.Observe(wanFail(now.Add(sec(21)), doms[1]))
	d.Evaluate(now.Add(sec(30)))
	d.Observe(wanOK(now.Add(sec(40)), doms[0]))
	d.Observe(wanOK(now.Add(sec(40)), doms[1]))
	d.Evaluate(now.Add(sec(41)))
	if evs := d.Evaluate(now.Add(sec(70))); len(evs) != 0 {
		t.Fatalf("hold must restart after a relapse: %+v", evs)
	}
	if evs := d.Evaluate(now.Add(sec(101))); !exits(evs, AnomalyUplink) {
		t.Fatalf("expected exit after a full hold: %+v", evs)
	}
}

func TestUplinkVetoedByAnyReply(t *testing.T) {
	d, _ := defaultLocalNet(t)
	doms := []string{"4:a", "4:b", "4:c", "4:d"}
	for _, dm := range doms {
		d.Observe(wanOK(t0, dm))
	}
	d.Observe(wanFail(t0.Add(sec(1)), doms[0]))
	d.Observe(wanFail(t0.Add(sec(2)), doms[1]))
	// One remote answers (even with an application error): network works.
	d.Observe(wanOK(t0.Add(sec(3)), doms[3]))
	d.Observe(wanFail(t0.Add(sec(4)), doms[2]))
	if evs := d.Evaluate(t0.Add(sec(5))); len(evs) != 0 {
		t.Fatalf("reply must veto: %+v", evs)
	}
}

func TestUplinkIgnoresTargetsAlreadyDead(t *testing.T) {
	d, _ := defaultLocalNet(t)
	// "4:dead" has been failing for days; it must not count as evidence.
	d.Observe(wanFail(t0.Add(-time.Hour), "4:dead"))
	d.Observe(wanOK(t0, "4:a"))
	d.Observe(wanOK(t0, "4:b"))
	d.Observe(wanOK(t0, "4:c"))
	d.Observe(wanFail(t0.Add(sec(1)), "4:dead"))
	d.Observe(wanFail(t0.Add(sec(2)), "4:a"))
	d.Observe(wanFail(t0.Add(sec(3)), "4:b"))
	if evs := d.Evaluate(t0.Add(sec(4))); len(evs) != 0 {
		t.Fatalf("dead target counted as evidence: %+v", evs)
	}
	d.Observe(wanFail(t0.Add(sec(5)), "4:c"))
	if evs := d.Evaluate(t0.Add(sec(6))); !enters(evs, AnomalyUplink) {
		t.Fatalf("three fresh domains must trigger: %+v", evs)
	}
}

func TestUplinkCannotJudgeWithFewDomains(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.cfg.References = nil
	d.Observe(wanOK(t0, "4:a"))
	d.Observe(wanOK(t0, "4:b"))
	if h := d.Health(t0); h.Uplink != "cannot_judge" || h.CanJudgeUplink {
		t.Fatalf("health: %+v", h)
	}
	d.Observe(wanFail(t0.Add(sec(1)), "4:a"))
	d.Observe(wanFail(t0.Add(sec(2)), "4:b"))
	if evs := d.Evaluate(t0.Add(sec(3))); len(evs) != 0 {
		t.Fatalf("two domains only: no automatic judgement: %+v", evs)
	}
}

func refFail(at time.Time, domain string) Observation {
	o := wanFail(at, domain)
	o.Reference = true
	return o
}

func refOK(at time.Time, domain string) Observation {
	o := wanOK(at, domain)
	o.Reference = true
	return o
}

// Two monitored networks are not enough evidence on their own; the
// reference points the self-check asked make up the rest.
func TestUplinkJudgedWithReferencePoints(t *testing.T) {
	d, _ := defaultLocalNet(t)
	if len(d.cfg.References) < 3 {
		t.Fatalf("default reference points: %v", d.cfg.References)
	}
	d.Observe(wanOK(t0, "4:a"))
	d.Observe(wanOK(t0, "4:b"))
	if h := d.Health(t0); !h.CanJudgeUplink || h.Uplink != "ok" {
		t.Fatalf("health with reference points: %+v", h)
	}
	d.Observe(wanFail(t0.Add(sec(1)), "4:a"))
	d.Observe(wanFail(t0.Add(sec(2)), "4:b"))
	if evs := d.Evaluate(t0.Add(sec(2))); len(evs) != 0 {
		t.Fatalf("before the self-check: %+v", evs)
	}
	for _, r := range []string{"4:r1", "4:r2", "4:r3"} {
		d.Observe(refFail(t0.Add(sec(3)), r))
	}
	if !enters(d.Evaluate(t0.Add(sec(4))), AnomalyUplink) {
		t.Fatal("both targets and every reference point failing: the uplink is down")
	}
	// Back: the targets answer again. The reference points have not been
	// asked since, which must not keep the outage open.
	d.Observe(wanOK(t0.Add(sec(30)), "4:a"))
	d.Observe(wanOK(t0.Add(sec(31)), "4:b"))
	var evs []LocalNetEvent
	for i := 31; i <= 31+2*60 && !exits(evs, AnomalyUplink); i++ {
		evs = d.Evaluate(t0.Add(sec(i)))
	}
	if !exits(evs, AnomalyUplink) {
		t.Fatal("the targets answer again: the outage must end")
	}
}

// A reference point that answers proves the network works: targets failing
// together are their own problem.
func TestReferencePointAnswerVetoesUplink(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.Observe(wanOK(t0, "4:a"))
	d.Observe(wanOK(t0, "4:b"))
	d.Observe(wanFail(t0.Add(sec(1)), "4:a"))
	d.Observe(wanFail(t0.Add(sec(2)), "4:b"))
	d.Observe(refFail(t0.Add(sec(3)), "4:r1"))
	d.Observe(refFail(t0.Add(sec(3)), "4:r2"))
	d.Observe(refOK(t0.Add(sec(3)), "4:r3"))
	if evs := d.Evaluate(t0.Add(sec(4))); len(evs) != 0 {
		t.Fatalf("a reference point answered: %+v", evs)
	}
}

func TestMinDomainsTunable(t *testing.T) {
	cfg := LocalNetConfigFrom(tunables.Set{})
	cfg.MinDomains = 2
	d := NewLocalNet(cfg, t0)
	d.Observe(wanOK(t0, "4:a"))
	d.Observe(wanOK(t0, "4:b"))
	d.Observe(wanFail(t0.Add(sec(1)), "4:a"))
	d.Observe(wanFail(t0.Add(sec(2)), "4:b"))
	if !enters(d.Evaluate(t0.Add(sec(3))), AnomalyUplink) {
		t.Fatal("min-domains=2 must trigger with two domains")
	}
}

func TestWANAnchors(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	at := t0
	for i := 0; i < cfg.AnchorFails; i++ {
		at = at.Add(15 * time.Second)
		d.Anchor(AnchorResult{At: at, Kind: AnchorWAN, Family: FamilyV4, Addr: "223.5.5.5"})
		d.Anchor(AnchorResult{At: at, Kind: AnchorWAN, Family: FamilyV4, Addr: "1.1.1.1"})
	}
	if evs := d.Evaluate(at); !enters(evs, AnomalyUplink) || evs[0].Anomaly.Suspected {
		t.Fatalf("all wan anchors down must enter (not merely suspected): %+v", evs)
	}
	if h := d.Health(at); !h.CanJudgeUplink || h.WANAnchors != 2 {
		t.Fatalf("health: %+v", h)
	}
	// A single anchor answering ends it at once.
	at = at.Add(15 * time.Second)
	d.Anchor(AnchorResult{At: at, Kind: AnchorWAN, Family: FamilyV4, Addr: "1.1.1.1", OK: true})
	if !exits(d.Evaluate(at), AnomalyUplink) {
		t.Fatal("an answering wan anchor must exit")
	}
	// One anchor down, the other fine: no anomaly even if domains fail.
	for _, dm := range []string{"4:a", "4:b", "4:c"} {
		d.Observe(wanOK(at, dm))
	}
	for i, dm := range []string{"4:a", "4:b", "4:c"} {
		d.Observe(wanFail(at.Add(sec(1+i)), dm))
	}
	if evs := d.Evaluate(at.Add(sec(5))); len(evs) != 0 {
		t.Fatalf("answering anchor overrides domain evidence: %+v", evs)
	}
}

func gw(at time.Time, ok bool) AnchorResult {
	return AnchorResult{At: at, Kind: AnchorAutoGateway, Family: FamilyV4, Addr: "192.168.1.1", OK: ok, RTT: time.Millisecond}
}

func TestGatewayDown(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	d.Anchor(gw(t0, true))
	at := t0
	for i := 0; i < cfg.AnchorFails-1; i++ {
		at = at.Add(15 * time.Second)
		d.Anchor(gw(at, false))
	}
	if evs := d.Evaluate(at); len(evs) != 0 {
		t.Fatalf("below anchor-fails: %+v", evs)
	}
	at = at.Add(15 * time.Second)
	d.Anchor(gw(at, false))
	evs := d.Evaluate(at)
	if !enters(evs, AnomalyGateway) {
		t.Fatalf("gateway down: %+v", evs)
	}
	if ex, _ := d.Affects(netscope.LAN, FamilyV4, false, false); !ex {
		t.Fatal("routed lan must be excluded")
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV4, false, false); !ex {
		t.Fatal("wan must be excluded")
	}
	if ex, _ := d.Affects(netscope.LAN, FamilyV4, true, false); ex {
		t.Fatal("on-link lan must still be judged")
	}
	if h := d.Health(at); h.Uplink != "lan_down" || h.GatewayOK {
		t.Fatalf("health: %+v", h)
	}
	// Recovery needs anchor-recovers consecutive successes.
	at = at.Add(15 * time.Second)
	d.Anchor(gw(at, true))
	if evs := d.Evaluate(at); len(evs) != 0 {
		t.Fatalf("one success is not enough: %+v", evs)
	}
	at = at.Add(15 * time.Second)
	d.Anchor(gw(at, true))
	if !exits(d.Evaluate(at), AnomalyGateway) {
		t.Fatal("gateway recovery")
	}
}

func TestGatewayVetoedByRoutedTraffic(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.Anchor(gw(t0, true))
	for i := 1; i <= 3; i++ {
		d.Anchor(gw(t0.Add(sec(15*i)), false))
	}
	// An on-link host answering proves nothing about the router.
	d.Observe(Observation{At: t0.Add(sec(40)), Scope: netscope.LAN, Family: FamilyV4, OnLink: true, Reachable: true})
	if !enters(d.Evaluate(t0.Add(sec(45))), AnomalyGateway) {
		t.Fatal("on-link success must not veto")
	}
	// A wan target answering through the router does.
	d.Observe(wanOK(t0.Add(sec(50)), "4:a"))
	if !exits(d.Evaluate(t0.Add(sec(51))), AnomalyGateway) {
		t.Fatal("routed success must veto")
	}
}

func TestGatewayThatNeverAnswersIsNotAnAnchor(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	at := t0
	for i := 0; i < 20; i++ {
		at = at.Add(15 * time.Second)
		d.Anchor(gw(at, false))
	}
	if evs := d.Evaluate(at); len(evs) != 0 {
		t.Fatalf("unresponsive router must not silence the instance: %+v", evs)
	}
	if h := d.Health(t0.Add(cfg.AnchorEnable)); h.GatewayNote != "unresponsive" {
		t.Fatalf("health note: %+v", h)
	}
	if h := d.Health(t0.Add(cfg.AnchorEnable - time.Second)); h.GatewayNote != "" {
		t.Fatalf("note too early: %+v", h)
	}
}

func TestContainerGatewayIgnored(t *testing.T) {
	d, _ := defaultLocalNet(t)
	r := gw(t0, true)
	r.Container = true
	d.Anchor(r)
	for i := 1; i <= 5; i++ {
		r = gw(t0.Add(sec(15*i)), false)
		r.Container = true
		d.Anchor(r)
	}
	if evs := d.Evaluate(t0.Add(2 * time.Minute)); len(evs) != 0 {
		t.Fatalf("container bridge must be ignored: %+v", evs)
	}
	if h := d.Health(t0); h.GatewayNote != "container" {
		t.Fatalf("health: %+v", h)
	}
}

func TestGatewayChangeStartsOver(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.Anchor(gw(t0, true))
	d.Anchor(gw(t0.Add(sec(15)), false))
	d.Anchor(gw(t0.Add(sec(30)), false))
	// New network: different gateway that never answered yet.
	d.Anchor(AnchorResult{At: t0.Add(sec(45)), Kind: AnchorAutoGateway, Family: FamilyV4, Addr: "10.0.0.1"})
	d.Anchor(AnchorResult{At: t0.Add(sec(60)), Kind: AnchorAutoGateway, Family: FamilyV4, Addr: "10.0.0.1"})
	d.Anchor(AnchorResult{At: t0.Add(sec(75)), Kind: AnchorAutoGateway, Family: FamilyV4, Addr: "10.0.0.1"})
	if evs := d.Evaluate(t0.Add(sec(80))); len(evs) != 0 {
		t.Fatalf("new gateway inherits nothing: %+v", evs)
	}
}

func TestExplicitLANAnchors(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	at := t0
	for i := 0; i < cfg.AnchorFails; i++ {
		at = at.Add(15 * time.Second)
		d.Anchor(AnchorResult{At: at, Kind: AnchorLAN, Family: FamilyV4, Addr: "192.168.1.2"})
	}
	evs := d.Evaluate(at)
	if !enters(evs, AnomalyGateway) || evs[0].Anomaly.Detail != "192.168.1.2" {
		t.Fatalf("explicit lan anchor: %+v", evs)
	}
	d.RemoveAnchor(AnchorLAN, FamilyV4, "192.168.1.2")
	if !exits(d.Evaluate(at), AnomalyGateway) {
		t.Fatal("removing the anchor ends the anomaly")
	}
}

func TestNoRoute(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.Anchor(AnchorResult{At: t0, Kind: AnchorAutoGateway, Family: FamilyV6, NoRoute: true})
	if !enters(d.Evaluate(t0), AnomalyNoRoute) {
		t.Fatal("no route")
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV6, false, false); !ex {
		t.Fatal("v6 wan excluded")
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV4, false, false); ex {
		t.Fatal("v4 untouched")
	}
}

func TestLocalDNS(t *testing.T) {
	d, _ := defaultLocalNet(t)
	// IP-level target answers: the network works.
	d.Observe(wanOK(t0, "4:a"))
	d.Observe(Observation{At: t0.Add(sec(1)), ByName: true, DNSFail: true, NetworkFail: true, Domain: "n:example.com"})
	if evs := d.Evaluate(t0.Add(sec(2))); len(evs) != 0 {
		t.Fatalf("one name is not enough: %+v", evs)
	}
	d.Observe(Observation{At: t0.Add(sec(3)), ByName: true, DNSFail: true, NetworkFail: true, Domain: "n:example.org"})
	if !enters(d.Evaluate(t0.Add(sec(4))), AnomalyDNS) {
		t.Fatal("dns down")
	}
	if ex, r := d.Affects(netscope.WAN, FamilyV4, false, true); !ex || r != "local_dns" {
		t.Fatalf("by-name target: %v %q", ex, r)
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV4, false, false); ex {
		t.Fatal("ip target must not be excluded by dns")
	}
	if !d.Health(t0.Add(sec(4))).DNSDown {
		t.Fatal("health dns")
	}
	// A name resolves again.
	d.Observe(Observation{At: t0.Add(sec(10)), Scope: netscope.WAN, Family: FamilyV4, ByName: true, Domain: "4:b", Reachable: true})
	if !exits(d.Evaluate(t0.Add(sec(11))), AnomalyDNS) {
		t.Fatal("dns recovery")
	}
}

func TestLocalDNSNeedsWorkingNetwork(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.Observe(Observation{At: t0, ByName: true, DNSFail: true, Domain: "n:a.com"})
	d.Observe(Observation{At: t0, ByName: true, DNSFail: true, Domain: "n:b.com"})
	if evs := d.Evaluate(t0.Add(sec(1))); len(evs) != 0 {
		t.Fatalf("no proof the network works: %+v", evs)
	}
}

func TestDisabledClearsEverything(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	for _, dm := range []string{"4:a", "4:b", "4:c"} {
		d.Observe(wanOK(t0, dm))
	}
	for _, dm := range []string{"4:a", "4:b", "4:c"} {
		d.Observe(wanFail(t0.Add(sec(1)), dm))
	}
	if !enters(d.Evaluate(t0.Add(sec(2))), AnomalyUplink) {
		t.Fatal("enter")
	}
	cfg.Enabled = false
	d.SetConfig(cfg)
	if !exits(d.Evaluate(t0.Add(sec(3))), AnomalyUplink) {
		t.Fatal("disabling must clear")
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV4, false, false); ex {
		t.Fatal("disabled detector excludes nothing")
	}
}

func TestGateTargets(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	d.Observe(wanOK(t0, "4:a"))
	d.Observe(wanOK(t0.Add(sec(1)), "4:b"))
	d.Observe(wanOK(t0.Add(sec(2)), "4:c"))
	d.Observe(wanOK(t0.Add(-time.Hour), "4:old"))
	got := d.GateTargets(FamilyV4, "4:c", 5, t0.Add(sec(3)))
	if len(got) != 2 || got[0] != "tcp:4:b" || got[1] != "tcp:4:a" {
		t.Fatalf("gate targets: %v", got)
	}
	if got := d.GateTargets(FamilyV4, "", 1, t0.Add(sec(3))); len(got) != 1 || got[0] != "tcp:4:c" {
		t.Fatalf("limit: %v", got)
	}
	if got := d.GateTargets(FamilyV4, "", 5, t0.Add(cfg.DomainTTL+sec(10))); len(got) != 0 {
		t.Fatalf("stale targets: %v", got)
	}
	if got := d.GateTargets(FamilyV6, "", 5, t0); got != nil {
		t.Fatalf("unknown family: %v", got)
	}
}

func TestFailureDomain(t *testing.T) {
	cases := []struct {
		host string
		addr string
		want string
	}{
		{"", "203.0.113.77", "4:203.0.113.0/24"},
		{"", "::ffff:203.0.113.77", "4:203.0.113.0/24"},
		{"", "2001:db8:1:2::1", "6:2001:db8:1::/48"},
		{"www.Example.COM.", "", "n:example.com"},
		{"a.b.example.com.cn", "", "n:example.com.cn"},
		{"localhost", "", "n:localhost"},
	}
	for _, c := range cases {
		var a netip.Addr
		if c.addr != "" {
			a = netip.MustParseAddr(c.addr)
		}
		if got := FailureDomain(c.host, a); got != c.want {
			t.Errorf("FailureDomain(%q,%q)=%q want %q", c.host, c.addr, got, c.want)
		}
	}
	if FamilyOf(netip.MustParseAddr("::ffff:1.2.3.4")) != FamilyV4 || FamilyOf(netip.MustParseAddr("::1")) != FamilyV6 {
		t.Error("FamilyOf")
	}
}

// The last reply and the first failure carry the same instant (rounds
// that started together): the cut is still recognised once nothing
// answers any more. It used to wait for a reply that never came.
func TestUplinkLastReplySameInstantAsFirstFailure(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	doms := []string{"4:a", "4:b", "4:c", "4:d"}
	for _, dm := range doms {
		d.Observe(wanOK(t0, dm))
	}
	at := t0.Add(sec(1))
	d.Observe(wanOK(at, doms[3]))
	d.Observe(wanFail(at, doms[0]))
	for i, dm := range doms {
		d.Observe(wanFail(at.Add(sec(1+i)), dm))
	}
	var evs []LocalNetEvent
	for s := 2; s < 10+int(cfg.Window/time.Second); s++ {
		evs = append(evs, d.Evaluate(at.Add(sec(s)))...)
	}
	if !enters(evs, string(AnomalyUplink)) {
		t.Fatalf("uplink cut not recognised: %+v", evs)
	}
}

// A failure stamped before the last reply but observed after it (rounds
// end out of order) is not the start of a cut: the cut starts with the
// first failure after the last reply, and is recognised once nothing
// answers any more. It used to wait for a reply that never came.
func TestUplinkFailureObservedAfterALaterReply(t *testing.T) {
	d, cfg := defaultLocalNet(t)
	doms := []string{"4:a", "4:b", "4:c", "4:d"}
	for _, dm := range doms {
		d.Observe(wanOK(t0, dm))
	}
	at := t0.Add(sec(1))
	d.Observe(wanOK(at.Add(time.Millisecond), doms[3]))
	d.Observe(wanFail(at, doms[0])) // arrives late
	for i, dm := range doms {
		d.Observe(wanFail(at.Add(sec(1+i)), dm))
	}
	var evs []LocalNetEvent
	for s := 2; s < 10+int(cfg.Window/time.Second); s++ {
		evs = append(evs, d.Evaluate(at.Add(sec(s)))...)
	}
	if !enters(evs, string(AnomalyUplink)) {
		t.Fatalf("uplink cut not recognised: %+v", evs)
	}
}

// The resolver stops answering while the targets checked by address keep
// answering: names failing in several zones between two replies are the
// resolver (dns_down), not the uplink. It used to be taken for an uplink
// outage in the seconds before the next reply.
func TestResolverFailuresAreNotAnUplinkCut(t *testing.T) {
	d, _ := defaultLocalNet(t)
	for _, dm := range []string{"4:a", "4:b", "4:c"} {
		d.Observe(wanOK(t0, dm))
	}
	// A name resolved just before the resolver stopped: no veto.
	d.Observe(Observation{At: t0, Scope: netscope.WAN, Family: FamilyV4, ByName: true, Domain: "4:d", Reachable: true})
	var evs []LocalNetEvent
	for i, zone := range []string{"n:zone1.test", "n:zone2.test", "n:zone3.test", "n:zone4.test"} {
		d.Observe(Observation{At: t0.Add(sec(1 + i)), Scope: netscope.WAN, ByName: true, DNSFail: true, NetworkFail: true, Domain: zone})
		evs = append(evs, d.Evaluate(t0.Add(sec(1+i)))...)
	}
	if enters(evs, AnomalyUplink) {
		t.Fatalf("resolver failures taken for an uplink cut: %+v", evs)
	}
	if !enters(evs, AnomalyDNS) {
		t.Fatalf("dns down not recognised: %+v", evs)
	}
}

// The same with an IPv6 family known only from its gateway: the resolver's
// failures are not evidence against an uplink this host never used.
func TestResolverFailuresAndAnUnusedFamily(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.f(FamilyV6) // e.g. an IPv6 default route, no IPv6 target
	for _, dm := range []string{"4:a", "4:b", "4:c"} {
		d.Observe(wanOK(t0, dm))
	}
	var evs []LocalNetEvent
	for i, zone := range []string{"zone1.test", "zone2.test", "zone3.test", "zone4.test"} {
		d.Observe(Observation{At: t0.Add(sec(1 + i)), Scope: netscope.WAN, ByName: true, DNSFail: true, NetworkFail: true, Domain: zone})
		evs = append(evs, d.Evaluate(t0.Add(sec(1+i)))...)
	}
	if enters(evs, AnomalyUplink) {
		t.Fatalf("an unused family's uplink called dead: %+v", evs)
	}
}
