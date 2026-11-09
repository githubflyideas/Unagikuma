// Package probe executes one check from one node and returns a vote.
//
// Contract: Run never panics, never blocks beyond Spec.Timeout
// (+ small slack) and always returns a Result whose Vote/Reason follow the
// failure mapping below. Probes are stateless; rate limiting, scheduling and
// quorum live in the engine.
package probe

import (
	"time"

	"github.com/githubflyideas/unagikuma/internal/keywords"
)

// Vote values.
type Vote string

const (
	VoteOK      Vote = "ok"
	VoteFail    Vote = "fail"
	VoteAbstain Vote = "abstain"
)

// Reason codes. Keep in sync with the UI copy table.
const (
	ReasonOK                = "ok"
	ReasonTimeout           = "timeout"              // fail, network-class
	ReasonHostUnreachable   = "host_unreachable"     // fail, network-class (ICMP 3/0,1)
	ReasonPortUnreachable   = "port_unreachable"     // fail (ICMP 3/3, ECONNREFUSED on UDP)
	ReasonConnRefused       = "conn_refused"         // fail (TCP RST)
	ReasonFiltered          = "filtered"             // fail by default (ICMP 3/9,10,13); on-filtered=abstain
	ReasonDNSResolve        = "dns_resolve"          // fail, network-class: resolver unreachable, timeout or SERVFAIL
	ReasonDNSNXDomain       = "dns_nxdomain"         // fail, protocol-class: authoritative NXDOMAIN/NODATA for the target name
	ReasonNoRoute           = "no_route"             // abstain, local: ENETUNREACH / EADDRNOTAVAIL / no route on this host
	ReasonTLS               = "tls_error"            // fail
	ReasonHTTPStatus        = "http_status"          // fail: unexpected status
	ReasonAssertFailed      = "assert_failed"        // fail: reply received, assertion failed
	ReasonProtoError        = "proto_error"          // fail: malformed/unexpected reply
	ReasonProtoReject       = "proto_reject"         // ok: protocol-level rejection proving liveness
	ReasonAuthError         = "auth_error"           // fail: service alive but refuses this probe
	ReasonRemoteRateLimited = "remote_rate_limited"  // ok + degraded (e.g. NTP KoD RATE)
	ReasonPrivilege         = "privilege"            // abstain
	ReasonSecretUnavailable = "secret_unavailable"   // abstain
	ReasonPlatform          = "platform"             // abstain: platform cannot observe the signal
	ReasonLocalRateLimited  = "local_rate_limited"   // abstain (engine)
	ReasonIsolated          = "isolated"             // abstain (engine)
	ReasonLocalNetwork      = "local_network"        // abstain (engine)
	ReasonLocalSuspend      = "local_suspend"        // abstain (engine)
	ReasonLocalStall        = "local_stall"          // abstain (engine)
	ReasonLocalDNS          = "local_dns"            // abstain (engine): this host's resolver is broken
	ReasonClockSkew         = "clock_skew"           // abstain (engine): this node's clock is off, time-based checks cannot judge
	ReasonExpiring          = "expiring"             // fail: the domain's registration expires (or expired)
	ReasonRegistry          = "registry_unavailable" // abstain: no registry answered the expiry lookup
	ReasonPortBusy          = "port_busy"            // abstain, local: a fixed source port (src-port=) is taken on this node
	ReasonInconclusive      = "inconclusive"         // abstain: no answer either way (a silent UDP port on a host that does not answer)
	ReasonConfig            = "config_error"         // abstain: spec invalid at run time
	ReasonInternal          = "internal_error"       // abstain
)

// NetworkClass reports whether a failure reason indicates the network path
// (not the remote service) failed — the signal used by local network anomaly
// detection. Protocol-class failures (HTTP status, assertions,
// port unreachable, NXDOMAIN...) prove the remote side is reachable and never
// count.
func NetworkClass(reason string) bool {
	switch reason {
	case ReasonTimeout, ReasonHostUnreachable, ReasonDNSResolve:
		return true
	}
	return false
}

// RemoteReachable reports whether a result proves the remote side answered at
// the network layer (any reply at all) — such evidence vetoes a local outage
// judgement.
func RemoteReachable(r Result) bool {
	if r.Vote == VoteOK {
		return true
	}
	switch r.Reason {
	case ReasonPortUnreachable, ReasonConnRefused, ReasonTLS, ReasonHTTPStatus,
		ReasonAssertFailed, ReasonProtoError, ReasonProtoReject, ReasonAuthError,
		ReasonRemoteRateLimited, ReasonDNSNXDomain, ReasonExpiring:
		return true
	}
	return false
}

// LocalAbstain reports whether a reason means this node could not judge
// (the node is not healthy for quorum purposes).
func LocalAbstain(reason string) bool {
	switch reason {
	case ReasonPrivilege, ReasonPlatform, ReasonSecretUnavailable, ReasonLocalRateLimited,
		ReasonIsolated, ReasonLocalNetwork, ReasonLocalSuspend, ReasonLocalStall,
		ReasonLocalDNS, ReasonClockSkew, ReasonNoRoute, ReasonConfig, ReasonInternal, ReasonPortBusy:
		return true
	}
	return false
}

// Spec is everything needed to run one check. Secrets in Opts are already
// resolved by the caller.
type Spec struct {
	Name    string            // monitor name (for messages, nonces)
	Type    string            // HTTP, TCP, PING, UDP (DNS/NTP/SNMP/SYSLOG normalised to UDP by config)
	Target  string            // URL, host:port, host, or host:keyword
	Opts    map[string]string // record options (type specific + common)
	Timeout time.Duration
	Dialect keywords.Dialect
	// Secrets are the resolved secret values in Target and Opts. Whatever
	// a probe records (evidence, detail) is scrubbed of them, wherever in
	// the exchange they appear: a URL query, a custom header, a body.
	Secrets []string `json:",omitempty"`
	// Share lets the node answer with the result of an identical check
	// that started a moment ago instead of running its own (share=yes).
	Share bool `json:",omitempty"`
}

// Field is one decoded protocol field for the packet viewer.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Evidence is the raw exchange (first 512 bytes each way) plus decoded fields.
// Callers MUST redact secrets before persisting.
type Evidence struct {
	Peer     string  `json:"peer,omitempty"`
	Request  []byte  `json:"request,omitempty"`
	Response []byte  `json:"response,omitempty"`
	Decoded  []Field `json:"decoded,omitempty"`
}

// Result of one probe execution.
type Result struct {
	Vote     Vote               `json:"vote"`
	Reason   string             `json:"reason"`
	Detail   string             `json:"detail"`  // human readable, one line, English (UI localises by reason)
	Latency  time.Duration      `json:"latency"` // time to the decisive reply
	Degraded bool               `json:"degraded,omitempty"`
	Depth    keywords.Depth     `json:"depth"`
	Metrics  map[string]float64 `json:"metrics,omitempty"` // e.g. ntp.offset_ms, ping.loss, cert.days
	Evidence *Evidence          `json:"evidence,omitempty"`
}
