package keywords

import "testing"

// The exact keyword sets offered by "eq ?".
var huawei = map[string]int{
	"biff": 512, "bootpc": 68, "bootps": 67, "discard": 9, "dns": 53, "dnsix": 90,
	"echo": 7, "mobilip-ag": 434, "mobilip-mn": 435, "nameserver": 42,
	"netbios-dgm": 138, "netbios-ns": 137, "netbios-ssn": 139, "ntp": 123,
	"rip": 520, "snmp": 161, "snmptrap": 162, "sunrpc": 111, "syslog": 514,
	"tacacs-ds": 65, "talk": 517, "tftp": 69, "time": 37, "who": 513, "xdmcp": 177,
}

var ciscoXE = map[string]int{
	"biff": 512, "bootpc": 68, "bootps": 67, "discard": 9, "dnsix": 195, "domain": 53,
	"echo": 7, "isakmp": 500, "mobile-ip": 434, "nameserver": 42, "netbios-dgm": 138,
	"netbios-ns": 137, "netbios-ss": 139, "non500-isakmp": 4500, "ntp": 123,
	"pim-auto-rp": 496, "rip": 520, "ripv6": 521, "snmp": 161, "snmptrap": 162,
	"sunrpc": 111, "syslog": 514, "tacacs": 49, "talk": 517, "tftp": 69, "time": 37,
	"who": 513, "xdmcp": 177,
}

func TestVendorSets(t *testing.T) {
	if len(huawei) != 25 || len(ciscoXE) != 28 {
		t.Fatalf("fixture sizes %d/%d", len(huawei), len(ciscoXE))
	}
	check := func(set map[string]int, d Dialect) {
		for kw, port := range set {
			r, err := Resolve(kw, d)
			if err != nil {
				t.Errorf("%s/%s: %v", d, kw, err)
				continue
			}
			if r.Port != port {
				t.Errorf("%s/%s: port %d, want %d", d, kw, r.Port, port)
			}
			if r.Entry == nil || !r.Entry.InDialect(d) {
				t.Errorf("%s/%s: not marked as listed by dialect", d, kw)
			}
		}
	}
	check(huawei, DialectHuawei)
	check(ciscoXE, DialectCisco)
}

func TestCounts(t *testing.T) {
	// union of Huawei 25 and IOS-XE 28 = 31 services.
	seen := map[*Entry]bool{}
	for kw := range huawei {
		e, _ := Lookup(kw)
		seen[e] = true
	}
	for kw := range ciscoXE {
		e, _ := Lookup(kw)
		seen[e] = true
	}
	if len(seen) != 30 { // dnsix 90/195 share one entry → 30 entries, 31 services
		t.Fatalf("union entries = %d, want 30 (31 services with dnsix twice)", len(seen))
	}
	var l3, l1, l0, later int
	for e := range seen {
		switch {
		case e.Depth == L0:
			l0++
		case e.Since == "v0.2":
			later++
		case e.Depth == L3:
			l3++
		default:
			l1++
		}
	}
	// dnsix counts twice (90 and 195) in the port table.
	if l3 != 12 || l1+1 != 10 || l0 != 6 || later != 3 {
		t.Fatalf("L3=%d L1=%d L0=%d v0.2=%d, want 12/10/6/3", l3, l1+1, l0, later)
	}
}

func TestDialectRules(t *testing.T) {
	if _, err := Resolve("dnsix", DialectAuto); err == nil {
		t.Error("dnsix should be ambiguous under auto")
	}
	if _, err := Resolve("dns", DialectASA); err == nil {
		t.Error("bare dns must be rejected under asa")
	}
	if r, _ := Resolve("domain", DialectASA); r.Port != 53 {
		t.Error("domain must be 53 on asa")
	}
	if r, _ := Resolve("radius", DialectASA); r.Port != 1645 {
		t.Error("asa radius must be 1645")
	}
	if r, _ := Resolve("radius", DialectAuto); r.Port != 1812 {
		t.Error("auto radius must be 1812")
	}
	if r, _ := Resolve("161", DialectAuto); r.Entry == nil || r.Entry.Keyword != "snmp" {
		t.Error("161 should be recognised as snmp")
	}
	if r, err := Resolve("40000", DialectAuto); err != nil || r.Entry != nil {
		t.Error("unknown port should resolve to raw")
	}
	if _, err := Resolve("nope", DialectAuto); err == nil {
		t.Error("unknown keyword must fail")
	}
}

func TestEffectiveDepth(t *testing.T) {
	ntp, _ := Lookup("ntp")
	boot, _ := Lookup("bootps")
	tac, _ := Lookup("tacacs")
	if ntp.EffectiveDepth(true) != L3 || ntp.EffectiveDepth(false) != L1 {
		t.Error("ntp depth")
	}
	if boot.EffectiveDepth(true) != L1 {
		t.Error("v0.2 template must run as L1 in v0.1")
	}
	if tac.EffectiveDepth(true) != L0 {
		t.Error("tacacs is L0")
	}
}
