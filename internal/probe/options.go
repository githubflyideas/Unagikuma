package probe

import "sort"

// netCommon are the options of every check that reaches its target over
// the network: the address family, reaching a cloud metadata address, and
// what a refusal by this host's own firewall counts as.
var netCommon = []string{"family", "allow-metadata", "on-filtered"}

// tlsOpts are the TLS options of a check over TCP.
var tlsOpts = []string{"tls", "servername", "insecure", "cert-days", "cert-critical-days"}

// OptionsFor lists the check options a monitor type takes, as the checks
// themselves accept them, so a form offers exactly those.
func OptionsFor(typ string) []string {
	set := map[string]bool{}
	add := func(keys ...string) {
		for _, k := range keys {
			set[k] = true
		}
	}
	addMap := func(m map[string]bool) {
		for k := range m {
			set[k] = true
		}
	}
	net, udp := true, false
	switch typ {
	case "HTTP":
		addMap(httpOpts)
		add("header.Authorization")
	case "TCP":
		addMap(tcpOpts)
	case "REDIS":
		add("user", "password", "role")
		add(tlsOpts...)
	case "GRPC":
		add("service")
		add(tlsOpts...)
	case "POSTGRES", "MYSQL":
		add("user", "password", "database", "ssl")
	case "PING":
		addMap(pingOpts)
		delete(set, "interval") // the spacing of echoes, not the monitor's interval; rarely wanted
	case "UDP":
		addMap(templateOpts["raw"])
		add("template", "retransmit")
		udp = true
	case "DNS", "NTP", "SNMP", "SYSLOG":
		tpl := map[string]string{"DNS": "dns", "NTP": "ntp", "SNMP": "snmp", "SYSLOG": "syslog"}[typ]
		addMap(templateOpts[tpl])
		add("retransmit")
		udp = true
	case "DOCKER":
		addMap(dockerOpts)
		net = false
	case "DOMAIN":
		addMap(domainOpts)
		net = false
	case "EXEC":
		addMap(execOpts)
		net = false
	default:
		net = false
	}
	// The check's own options first, then those every network check has.
	out := make([]string, 0, len(set)+4)
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	if udp {
		// Answers that can be used to reflect traffic at someone are only
		// asked of servers marked as one's own.
		out = append(out, "authorized")
	}
	if net {
		out = append(out, netCommon...)
	}
	return out
}
