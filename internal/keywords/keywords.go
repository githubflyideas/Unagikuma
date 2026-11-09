// Package keywords is the UDP port keyword catalog: every UDP port keyword
// that Huawei VRP / H3C Comware and Cisco IOS/IOS-XE/NX-OS/IOS-XR/ASA offer
// for "acl ... udp destination-port eq ?", plus unagikuma's own aliases.
//
// The catalog is the single source of truth for keyword → port resolution,
// probe template selection and the advertised detection depth.
package keywords

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Depth is the honest detection depth of a probe.
type Depth string

const (
	L3 Depth = "L3" // protocol-level: parse reply, assert fields
	L2 Depth = "L2" // reply-level: any well-formed reply
	L1 Depth = "L1" // delivery-level: sent, no ICMP port-unreachable (weak)
	L0 Depth = "L0" // not applicable: TCP-only, multicast-only or obsolete
)

// Dialect selects vendor semantics for ambiguous keywords.
type Dialect string

const (
	DialectAuto   Dialect = "auto"
	DialectHuawei Dialect = "huawei" // Huawei VRP and H3C Comware
	DialectCisco  Dialect = "cisco"  // IOS, IOS-XE, NX-OS
	DialectASA    Dialect = "asa"
	DialectXR     Dialect = "xr"
)

// ParseDialect validates a $DIALECT value.
func ParseDialect(s string) (Dialect, error) {
	switch d := Dialect(strings.ToLower(strings.TrimSpace(s))); d {
	case "", DialectAuto:
		return DialectAuto, nil
	case DialectHuawei, DialectCisco, DialectASA, DialectXR:
		return d, nil
	}
	return "", fmt.Errorf("unknown dialect %q (want auto, huawei, cisco, asa or xr)", s)
}

// Source vendors that list a keyword.
const (
	SrcHuawei = "huawei" // Huawei VRP5/VRP8 and H3C Comware V3-V9
	SrcCisco  = "cisco"  // IOS / IOS-XE / NX-OS
	SrcXR     = "xr"
	SrcASA    = "asa"
	SrcIANA   = "iana" // unagikuma-only names / IANA service names
)

// Entry describes one UDP service keyword.
type Entry struct {
	Keyword   string   // canonical unagikuma keyword
	Aliases   []string // other accepted spellings (dialect independent)
	Port      int      // default (IANA-aligned) port
	Service   string   // human readable service name (en)
	ServiceZH string   // human readable service name (zh)
	Sources   []string // vendors that list it in "eq ?"
	// Template is the probe template id; "l1" = generic delivery check,
	// "" for L0 keywords that cannot be probed.
	Template string
	// Depth is what the full template delivers; Since is the version that
	// delivers it. Before that version the keyword runs as L1.
	Depth Depth
	Since string // "v0.1", "v0.1-p1", "v0.2", "v0.3"
	// Stateful templates create state on the target (IKE half-open SA, DHCP
	// lease, injected trap): enforced minimum interval applies.
	Stateful bool
	// Reflective templates are known reflection/amplification vectors:
	// require authorized=yes.
	Reflective bool
	// Privileged templates need to bind a privileged port.
	Privileged bool
	// DefaultInterval overrides the SOA interval when the user sets none
	// (e.g. 5m for IKE, 15m for snmptrap). 0 = SOA default.
	DefaultIntervalSec int
	// Suggest is shown for L0 keywords (and as advice for weak ones).
	Suggest string
	Note    string
}

// v01 reports whether the full template is part of v0.1 (P0 or P1).
func (e *Entry) v01() bool { return e.Since == "v0.1" || e.Since == "v0.1-p1" }

// EffectiveDepth is the depth the current build delivers for this keyword.
// built reports whether the template is implemented in this build (P1
// templates may slip); unbuilt templates degrade to L1.
func (e *Entry) EffectiveDepth(built bool) Depth {
	if e.Depth == L0 {
		return L0
	}
	if !built || !e.v01() {
		return L1
	}
	return e.Depth
}

var catalog = []*Entry{
	{Keyword: "echo", Port: 7, Service: "Echo", ServiceZH: "回显", Sources: []string{SrcHuawei, SrcCisco, SrcXR}, Template: "echo", Depth: L3, Since: "v0.1", Reflective: true, Note: "reflector: monitor your own devices only"},
	{Keyword: "discard", Port: 9, Service: "Discard", ServiceZH: "丢弃服务", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "time", Port: 37, Service: "Time (RFC 868)", ServiceZH: "时间协议", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "time", Depth: L3, Since: "v0.1-p1"},
	{Keyword: "nameserver", Port: 42, Service: "IEN-116 host name server", ServiceZH: "IEN-116 名称服务", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Depth: L0, Since: "v0.1", Suggest: "obsolete; on Windows port 42 is WINS replication over TCP — use TCP host:42"},
	{Keyword: "tacacs", Port: 49, Service: "TACACS", ServiceZH: "TACACS", Sources: []string{SrcCisco, SrcXR, SrcASA}, Depth: L0, Since: "v0.1", Suggest: "TACACS+ runs on TCP 49 — use TCP host:49"},
	{Keyword: "dns", Aliases: []string{"domain"}, Port: 53, Service: "Domain Name Service", ServiceZH: "域名服务", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "dns", Depth: L3, Since: "v0.1", Reflective: true},
	{Keyword: "tacacs-ds", Port: 65, Service: "TACACS database service", ServiceZH: "TACACS 数据库服务", Sources: []string{SrcHuawei}, Depth: L0, Since: "v0.1", Suggest: "obsolete service"},
	{Keyword: "bootps", Port: 67, Service: "DHCP/BOOTP server", ServiceZH: "DHCP 服务器", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "dhcp", Depth: L3, Since: "v0.2", Stateful: true, Privileged: true, DefaultIntervalSec: 300},
	{Keyword: "bootpc", Port: 68, Service: "DHCP/BOOTP client", ServiceZH: "DHCP 客户端", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Depth: L0, Since: "v0.1", Suggest: "client port — monitor the server side with :bootps"},
	{Keyword: "tftp", Port: 69, Service: "Trivial File Transfer", ServiceZH: "TFTP", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "tftp", Depth: L3, Since: "v0.1"},
	{Keyword: "sunrpc", Aliases: []string{"portmapper", "rpcbind"}, Port: 111, Service: "ONC RPC portmapper", ServiceZH: "RPC 端口映射", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "sunrpc", Depth: L3, Since: "v0.1", Reflective: true},
	{Keyword: "ntp", Port: 123, Service: "Network Time Protocol", ServiceZH: "网络时间协议", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "ntp", Depth: L3, Since: "v0.1", Reflective: true},
	{Keyword: "netbios-ns", Port: 137, Service: "NetBIOS name service", ServiceZH: "NetBIOS 名称服务", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "netbios-ns", Depth: L3, Since: "v0.1-p1", Reflective: true},
	{Keyword: "netbios-dgm", Port: 138, Service: "NetBIOS datagram service", ServiceZH: "NetBIOS 数据报服务", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "netbios-ssn", Aliases: []string{"netbios-ss"}, Port: 139, Service: "NetBIOS session service", ServiceZH: "NetBIOS 会话服务", Sources: []string{SrcHuawei, SrcCisco, SrcXR}, Depth: L0, Since: "v0.1", Suggest: "session service is TCP — use TCP host:139"},
	{Keyword: "snmp", Port: 161, Service: "SNMP", ServiceZH: "SNMP", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "snmp", Depth: L3, Since: "v0.1"},
	{Keyword: "snmptrap", Port: 162, Service: "SNMP trap receiver", ServiceZH: "SNMP Trap 接收器", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "snmptrap", Depth: L1, Since: "v0.1", Stateful: true, DefaultIntervalSec: 900, Note: "injects a trap into the manager every probe; INFORM (L3) arrives in v0.2"},
	{Keyword: "xdmcp", Port: 177, Service: "X Display Manager Control Protocol", ServiceZH: "X 显示管理器", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "xdmcp", Depth: L3, Since: "v0.1-p1"},
	{Keyword: "mobilip-ag", Aliases: []string{"mobile-ip"}, Port: 434, Service: "Mobile IP agent", ServiceZH: "Mobile IP 代理", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "l1", Depth: L2, Since: "v0.2", Note: "registration request; needs lab verification"},
	{Keyword: "mobilip-mn", Port: 435, Service: "Mobile IP mobile node", ServiceZH: "Mobile IP 移动节点", Sources: []string{SrcHuawei}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "pim-auto-rp", Port: 496, Service: "PIM Auto-RP", ServiceZH: "PIM Auto-RP", Sources: []string{SrcCisco, SrcXR, SrcASA}, Depth: L0, Since: "v0.1", Suggest: "multicast only (224.0.1.39/40) — monitor PIM neighbours via SNMP"},
	{Keyword: "isakmp", Port: 500, Service: "IKE / ISAKMP", ServiceZH: "IKE", Sources: []string{SrcCisco, SrcXR, SrcASA}, Template: "ike", Depth: L3, Since: "v0.1", Stateful: true, DefaultIntervalSec: 300, Note: "leaves a half-open SA and a log line on the responder"},
	{Keyword: "biff", Port: 512, Service: "comsat mail notification", ServiceZH: "邮件通知（comsat）", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "who", Port: 513, Service: "rwho", ServiceZH: "rwho", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "syslog", Port: 514, Service: "Syslog", ServiceZH: "Syslog", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "syslog", Depth: L1, Since: "v0.1", DefaultIntervalSec: 300, Note: "writes one log line per probe; also add a TCP/TLS syslog check for L3"},
	{Keyword: "talk", Port: 517, Service: "talk", ServiceZH: "talk", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "rip", Port: 520, Service: "Routing Information Protocol", ServiceZH: "RIP 路由协议", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "rip", Depth: L3, Since: "v0.1", Reflective: true},
	{Keyword: "ripv6", Aliases: []string{"ripng"}, Port: 521, Service: "RIPng", ServiceZH: "RIPng", Sources: []string{SrcCisco}, Template: "l1", Depth: L3, Since: "v0.2", Note: "IPv6 only"},
	{Keyword: "non500-isakmp", Aliases: []string{"ipsec-nat-t"}, Port: 4500, Service: "IKE NAT-T", ServiceZH: "IKE NAT 穿越", Sources: []string{SrcCisco}, Template: "ike-natt", Depth: L3, Since: "v0.1-p1", Stateful: true, DefaultIntervalSec: 300},
	// dnsix: Huawei/H3C 90, Cisco family 195 — resolved per dialect.
	{Keyword: "dnsix", Port: 0, Service: "DNSIX", ServiceZH: "DNSIX", Sources: []string{SrcHuawei, SrcCisco, SrcXR, SrcASA}, Template: "l1", Depth: L1, Since: "v0.1", Note: "Huawei/H3C: 90 (security attribute token map); Cisco: 195 (network level module audit)"},
	// ASA / IOS-XR extras (recognised in v0.1; L3 in v0.2 where applicable).
	{Keyword: "radius", Port: 1812, Service: "RADIUS authentication", ServiceZH: "RADIUS 认证", Sources: []string{SrcASA}, Template: "l1", Depth: L3, Since: "v0.2", Note: "ASA literal = 1645; Status-Server needs a shared secret"},
	{Keyword: "radius-acct", Port: 1813, Service: "RADIUS accounting", ServiceZH: "RADIUS 计费", Sources: []string{SrcASA}, Template: "l1", Depth: L3, Since: "v0.2", Note: "ASA literal = 1646"},
	{Keyword: "kerberos", Port: 88, Service: "Kerberos", ServiceZH: "Kerberos", Sources: []string{SrcASA}, Template: "l1", Depth: L3, Since: "v0.2", Note: "ASA literal = 750 (ASA <= 9.20)"},
	{Keyword: "sip", Port: 5060, Service: "SIP", ServiceZH: "SIP", Sources: []string{SrcASA}, Template: "l1", Depth: L3, Since: "v0.2", Stateful: true},
	{Keyword: "nfs", Port: 2049, Service: "NFS (RPC NULL)", ServiceZH: "NFS", Sources: []string{SrcASA}, Template: "l1", Depth: L3, Since: "v0.2"},
	{Keyword: "secureid-udp", Port: 5510, Service: "SecurID", ServiceZH: "SecurID", Sources: []string{SrcASA}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "pcanywhere-status", Port: 5632, Service: "pcAnywhere status", ServiceZH: "pcAnywhere 状态", Sources: []string{SrcASA}, Template: "l1", Depth: L1, Since: "v0.1"},
	{Keyword: "cifs", Port: 3020, Service: "CIFS (ASA literal)", ServiceZH: "CIFS", Sources: []string{SrcASA}, Depth: L0, Since: "v0.1", Suggest: "use TCP host:445"},
	{Keyword: "http", Aliases: []string{"www"}, Port: 80, Service: "HTTP (ASA literal)", ServiceZH: "HTTP", Sources: []string{SrcASA}, Depth: L0, Since: "v0.1", Suggest: "use an HTTP check"},
	{Keyword: "vxlan", Port: 4789, Service: "VXLAN", ServiceZH: "VXLAN", Sources: []string{SrcASA}, Depth: L0, Since: "v0.1", Suggest: "data-plane encapsulation, no reply — monitor the VTEP via PING/SNMP"},
	{Keyword: "bfd", Port: 3784, Service: "BFD control", ServiceZH: "BFD", Sources: []string{SrcXR}, Depth: L0, Since: "v0.1", Suggest: "needs a session — use SNMP or telemetry"},
	{Keyword: "ldp", Port: 646, Service: "LDP discovery", ServiceZH: "LDP 发现", Sources: []string{SrcXR}, Template: "l1", Depth: L1, Since: "v0.1"},
}

var byName = map[string]*Entry{}
var byPort = map[int]*Entry{}

func init() {
	for _, e := range catalog {
		for _, n := range append([]string{e.Keyword}, e.Aliases...) {
			if _, dup := byName[n]; dup {
				panic("keywords: duplicate name " + n)
			}
			byName[n] = e
		}
		if e.Port != 0 {
			if _, dup := byPort[e.Port]; !dup {
				byPort[e.Port] = e
			}
		}
	}
	// dnsix resolves per dialect; make both ports discoverable.
	byPort[90] = byName["dnsix"]
	byPort[195] = byName["dnsix"]
}

// All returns the catalog sorted by port (dnsix listed at 90).
func All() []*Entry {
	out := append([]*Entry(nil), catalog...)
	sort.SliceStable(out, func(i, j int) bool { return sortPort(out[i]) < sortPort(out[j]) })
	return out
}

func sortPort(e *Entry) int {
	if e.Keyword == "dnsix" {
		return 90
	}
	return e.Port
}

// Lookup finds an entry by keyword or alias (case-insensitive).
func Lookup(word string) (*Entry, bool) {
	e, ok := byName[strings.ToLower(word)]
	return e, ok
}

// ByPort finds the entry registered for a port number.
func ByPort(port int) (*Entry, bool) {
	e, ok := byPort[port]
	return e, ok
}

// Resolution is the outcome of resolving "host:word".
type Resolution struct {
	Entry *Entry // nil for an unknown numeric port (raw UDP)
	Port  int
	Hint  string // non-fatal note for check/UI ("ASA literal 1645 used", ...)
}

// Resolve maps a keyword or numeric port to a port under a dialect.
// Errors are configuration errors (ambiguous keyword, unknown keyword).
func Resolve(word string, d Dialect) (Resolution, error) {
	w := strings.ToLower(strings.TrimSpace(word))
	if w == "" {
		return Resolution{}, fmt.Errorf("missing port or keyword")
	}
	if n, err := strconv.Atoi(w); err == nil {
		if n < 1 || n > 65535 {
			return Resolution{}, fmt.Errorf("port %d out of range", n)
		}
		r := Resolution{Port: n}
		if e, ok := byPort[n]; ok {
			r.Entry = e
			r.Hint = fmt.Sprintf("port %d recognised as %q", n, e.Keyword)
		}
		return r, nil
	}
	// ASA: "dns" is an alias of dnsix (195); 53 must be written "domain".
	if w == "dns" && d == DialectASA {
		return Resolution{}, fmt.Errorf(`on ASA "dns" means dnsix (195); write "domain" for 53 or "dnsix" for 195`)
	}
	e, ok := byName[w]
	if !ok {
		return Resolution{}, fmt.Errorf("unknown UDP keyword %q (run `unagikuma keywords` for the list)", word)
	}
	r := Resolution{Entry: e, Port: e.Port}
	switch e.Keyword {
	case "dnsix":
		switch d {
		case DialectHuawei:
			r.Port = 90
		case DialectCisco, DialectASA, DialectXR:
			r.Port = 195
		default:
			return Resolution{}, fmt.Errorf(`"dnsix" is ambiguous (Huawei/H3C 90, Cisco 195): set $DIALECT or write the port number`)
		}
	case "radius":
		if d == DialectASA {
			r.Port, r.Hint = 1645, "ASA literal: radius = 1645"
		} else {
			r.Hint = "IANA port 1812 used (ASA uses 1645: set $DIALECT asa)"
		}
	case "radius-acct":
		if d == DialectASA {
			r.Port, r.Hint = 1646, "ASA literal: radius-acct = 1646"
		} else {
			r.Hint = "IANA port 1813 used (ASA uses 1646: set $DIALECT asa)"
		}
	case "kerberos":
		if d == DialectASA {
			r.Port, r.Hint = 750, "ASA literal: kerberos = 750 (ASA <= 9.20)"
		} else {
			r.Hint = "IANA port 88 used (ASA <= 9.20 uses 750: set $DIALECT asa)"
		}
	}
	return r, nil
}

// InDialect reports whether a keyword is listed by the vendor family of d.
func (e *Entry) InDialect(d Dialect) bool {
	want := map[Dialect]string{DialectHuawei: SrcHuawei, DialectCisco: SrcCisco, DialectASA: SrcASA, DialectXR: SrcXR}[d]
	if want == "" {
		return true
	}
	for _, s := range e.Sources {
		if s == want {
			return true
		}
	}
	return false
}
