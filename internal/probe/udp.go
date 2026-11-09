package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/keywords"
)

// udpPlan is what a UDP spec resolves to.
type udpPlan struct {
	host     string
	port     int
	entry    *keywords.Entry
	keyword  string
	template string // implemented template to run
	depth    keywords.Depth
	// fallback: the keyword's template is not implemented in this build
	// and the L1 delivery check runs instead.
	fallback bool
}

// templateOpts lists the options of each implemented template.
var templateOpts = map[string]map[string]bool{
	"echo":     {"size": true},
	"dns":      {"query": true, "qtype": true, "rcode": true, "expect": true, "expect-all": true, "aa": true, "min-answers": true, "recurse": true},
	"ntp":      {"max-offset": true, "max-stratum": true},
	"snmp":     {"version": true, "community": true, "oid": true, "expect": true, "expect-num": true},
	"snmptrap": {"community": true, "host-check": true},
	"time":     {"max-offset": true},
	"tftp":     {"file": true, "expect": true},
	"sunrpc":   {"expect-program": true},
	"syslog":   {"host-check": true},
	"l1":       {"host-check": true},
	"raw": {"send": true, "send-hex": true, "expect": true, "expect-hex": true, "expect-regex": true, "expect-len": true,
		"accept-from": true, "src-port": true, "host-check": true},
}

// planUDP resolves keyword, port, template and depth.
func planUDP(s Spec) (udpPlan, error) {
	host, word, err := net.SplitHostPort(s.Target)
	if err != nil || host == "" {
		return udpPlan{}, errors.New("UDP target must be host:keyword or host:port")
	}
	res, err := keywords.Resolve(word, s.Dialect)
	if err != nil {
		return udpPlan{}, err
	}
	p := udpPlan{host: host, port: res.Port, entry: res.Entry, template: "raw"}
	if res.Entry != nil {
		p.keyword = res.Entry.Keyword
		if res.Entry.Template != "" {
			p.template = res.Entry.Template
		}
	}
	if t := strings.ToLower(s.Opts["template"]); t != "" {
		p.template = t
	}
	switch {
	case p.entry != nil && p.entry.Template == "" && s.Opts["template"] == "":
		p.depth = keywords.L0
	case p.template == "raw":
		p.depth = rawDepth(opts(s.Opts))
	case !Built(p.template):
		// Not implemented in this build: honest L1 delivery check.
		p.template, p.depth, p.fallback = "l1", keywords.L1, true
	case p.entry != nil && p.entry.Template == p.template:
		p.depth = p.entry.EffectiveDepth(true)
	default:
		p.depth = templateDepth(p.template)
	}
	return p, nil
}

func templateDepth(t string) keywords.Depth {
	switch t {
	case "l1", "syslog", "snmptrap":
		return keywords.L1
	}
	return keywords.L3
}

func rawDepth(o opts) keywords.Depth {
	if e := o.str("expect", ""); e == "none" || (e == "" && !o.has("expect-hex") && !o.has("expect-regex") && !o.has("expect-len")) {
		return keywords.L1
	}
	return keywords.L2
}

func validateUDP(s Spec, o opts) error {
	p, err := planUDP(s)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"template": true, "retransmit": true}
	for k := range templateOpts[p.template] {
		allowed[k] = true
	}
	if p.fallback {
		// A template this build does not implement yet: its options are
		// accepted and ignored until it does.
		for k := range o {
			allowed[k] = true
		}
	}
	if err := rejectUnknown(o, allowed); err != nil {
		return err
	}
	if t := s.Opts["template"]; t != "" {
		if _, known := templateOpts[strings.ToLower(t)]; !known && !isKnownTemplate(t) {
			return fmt.Errorf("unknown template %q", t)
		}
	}
	if _, err := o.intOpt("retransmit", 1, 0, 5); err != nil {
		return err
	}
	if v := o.str("host-check", "on"); v != "on" && v != "off" {
		return fmt.Errorf("host-check=%s: use on or off", v)
	}
	switch p.template {
	case "dns":
		return validateDNS(o)
	case "ntp", "time":
		if _, err := o.durOpt("max-offset", 0); err != nil {
			return err
		}
		_, err := o.intOpt("max-stratum", 15, 1, 16)
		return err
	case "snmp":
		return validateSNMP(o)
	case "raw":
		return validateRaw(o)
	case "echo":
		_, err := o.intOpt("size", 24, 4, 1024)
		return err
	case "tftp":
		if v := o.str("expect", "any"); v != "any" && v != "data" {
			return errors.New("tftp expect must be any or data")
		}
	case "sunrpc":
		if v := o.str("expect-program", ""); v != "" {
			if _, _, ok := rpcProgram(v); !ok {
				return fmt.Errorf("unknown RPC program %q (nfs, mountd, nlockmgr, status, rquotad or a number)", v)
			}
		}
	}
	return nil
}

func isKnownTemplate(t string) bool {
	for _, e := range keywords.All() {
		if e.Template == strings.ToLower(t) {
			return true
		}
	}
	return false
}

func runUDP(ctx context.Context, s Spec) Result {
	o := opts(s.Opts)
	p, _ := planUDP(s)
	if p.depth == keywords.L0 {
		hint := ""
		if p.entry != nil {
			hint = p.entry.Suggest
		}
		return abstain(ReasonConfig, "%s cannot be checked over UDP: %s", p.keyword, hint)
	}
	var r Result
	switch p.template {
	case "dns":
		r = runDNS(ctx, p, o)
	case "ntp":
		r = runNTP(ctx, p, o)
	case "snmp":
		r = runSNMP(ctx, p, o)
	case "echo":
		r = runEcho(ctx, p, o)
	case "time":
		r = runTime(ctx, p, o)
	case "tftp":
		r = runTFTP(ctx, p, o)
	case "sunrpc":
		r = runSunRPC(ctx, p, o)
	case "raw":
		r = runRaw(ctx, p, o)
	case "syslog":
		r = runL1(ctx, p, o, syslogMessage())
	case "snmptrap":
		r = runL1(ctx, p, o, snmpTrap(o.str("community", "public")))
	default:
		r = runL1(ctx, p, o, []byte("unagikuma-probe\n"))
	}
	r.Depth = p.depth
	return r
}

// udpSock is a connected UDP socket to the target.
type udpSock struct {
	c      *net.UDPConn
	peer   string
	errsOK bool // ICMP errors are reported on this platform/socket
}

func dialUDP(ctx context.Context, p udpPlan, o opts) (*udpSock, Result, bool) {
	ip, err := resolve(ctx, p.host, o)
	if err != nil {
		return nil, classify(err, true, o), false
	}
	raddr := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(p.port)))
	c, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		r := classify(err, true, o)
		r.Evidence = &Evidence{Peer: raddr.String()}
		return nil, r, false
	}
	return &udpSock{c: c, peer: raddr.String(), errsOK: enableUDPErrors(c)}, Result{}, true
}

// exchange sends req and waits for a datagram accepted by accept,
// retransmitting up to `retransmit` times within the deadline. Datagrams
// that are not accepted (stale, spoofed, wrong id) are ignored.
//
// The latency is that of one request when the first one was answered. A
// reply that comes after a retransmission may answer any of the copies
// sent, so its latency counts from the first one: too long rather than a
// misleadingly short time for a service that needed a second try.
func (u *udpSock) exchange(ctx context.Context, req []byte, retransmit int, accept func([]byte) bool) ([]byte, time.Duration, error) {
	deadline, _ := ctx.Deadline()
	total := time.Until(deadline)
	slot := total / time.Duration(retransmit+1)
	buf := make([]byte, 65535)
	start := time.Now()
	for attempt := 0; attempt <= retransmit; attempt++ {
		sentAt := time.Now()
		if _, err := u.c.Write(req); err != nil {
			return nil, 0, err
		}
		until := sentAt.Add(slot)
		if attempt == retransmit || until.After(deadline) {
			until = deadline
		}
		for {
			_ = u.c.SetReadDeadline(until)
			n, err := u.c.Read(buf)
			if err != nil {
				if isTimeout(err) || errors.Is(err, os.ErrDeadlineExceeded) {
					break
				}
				return nil, time.Since(start), err
			}
			if accept == nil || accept(buf[:n]) {
				return append([]byte(nil), buf[:n]...), replyLatency(attempt, start, sentAt), nil
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, time.Since(start), context.DeadlineExceeded
}

// replyLatency is the latency of a reply that arrived during the given
// attempt (0 = the first send), see exchange.
func replyLatency(attempt int, first, last time.Time) time.Duration {
	if attempt > 0 {
		return time.Since(first)
	}
	return time.Since(last)
}

func (u *udpSock) Close() { u.c.Close() }

// udpResult builds the common result of a request/response template.
func udpFail(u *udpSock, err error, req []byte, o opts, hint string) Result {
	r := classify(err, true, o)
	if r.Reason == ReasonTimeout && hint != "" {
		r.Detail = oneLine(r.Detail + " (" + hint + ")")
	}
	r.Evidence = newEvidence(u.peer, req, nil)
	return r
}

// hostCheck asks whether a host is there: one ICMP echo, or TCP when ping
// is not allowed for this process (a variable for tests).
var hostCheck = func(ctx context.Context, ip netip.Addr) Result {
	return runPing(ctx, Spec{Type: "PING", Target: ip.String(), Opts: map[string]string{"count": "1", "fallback": "tcp"}})
}

// runL1 is the weak delivery check: send a harmless datagram; an ICMP port
// unreachable proves the port is closed. Silence alone proves nothing: a
// host that is switched off cannot send port unreachable either. So the
// host is checked alongside (ICMP echo, or TCP when ping is not allowed)
// and silence counts as "probably open" only while the host answers. A
// host that does not answer leaves the check undecided rather than green;
// one reported unreachable fails it. host-check=off keeps the old reading
// for hosts that answer nothing at all. It is labelled L1 everywhere.
func runL1(ctx context.Context, p udpPlan, o opts, payload []byte) Result {
	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	if !u.errsOK {
		return abstain(ReasonPlatform, "this platform cannot observe ICMP port unreachable on UDP sockets")
	}
	var alive chan Result
	if o.str("host-check", "on") != "off" {
		alive = make(chan Result, 1)
		ip := u.c.RemoteAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
		check := hostCheck
		go func() { alive <- check(ctx, ip) }()
	}
	_, lat, err := u.exchange(ctx, payload, 0, func([]byte) bool { return true })
	ev := newEvidence(u.peer, payload, nil)
	switch {
	case err == nil:
		res := ok("a reply arrived")
		res.Latency, res.Evidence = lat, ev
		return res
	case errors.Is(err, context.DeadlineExceeded):
		res := ok("no ICMP port unreachable (weak signal)")
		res.Evidence = ev
		if alive == nil {
			return res
		}
		h := <-alive
		switch {
		case h.Vote == VoteOK:
			res.Detail = "no ICMP port unreachable and the host answers (weak signal)"
			return res
		case h.Vote == VoteFail && h.Reason == ReasonHostUnreachable:
			res = fail(ReasonHostUnreachable, "no reply, and the host is unreachable: %s", h.Detail)
		default:
			res = abstain(ReasonInconclusive, "no reply, and the host does not answer ping either: cannot tell whether the port is open (host-check=off accepts silence)")
		}
		res.Evidence = ev
		return res
	}
	res := classify(err, true, o)
	res.Evidence = ev
	return res
}

func syslogMessage() []byte {
	host, _ := os.Hostname()
	if host == "" {
		host = "-"
	}
	return []byte(fmt.Sprintf("<14>1 %s %s unagikuma - - - unagikuma-probe", time.Now().UTC().Format(time.RFC3339), host))
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func runEcho(ctx context.Context, p udpPlan, o opts) Result {
	size, _ := o.intOpt("size", 24, 4, 1024)
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	nonce := randBytes(size)
	// Only the echo of this request counts: a late or foreign datagram is
	// set aside and the wait goes on. If nothing else came, the last such
	// datagram shows the service answers, but not with an echo.
	var other []byte
	resp, lat, err := u.exchange(ctx, nonce, retr, func(b []byte) bool {
		if bytes.Equal(b, nonce) {
			return true
		}
		other = append(other[:0], b...)
		return false
	})
	if err != nil && other != nil && errors.Is(err, context.DeadlineExceeded) {
		res := fail(ReasonAssertFailed, "the reply is not an echo of the request")
		res.Evidence = newEvidence(u.peer, nonce, other)
		return res
	}
	if err != nil {
		return udpFail(u, err, nonce, o, "")
	}
	res := ok("echo matched")
	res.Latency, res.Evidence = lat, newEvidence(u.peer, nonce, resp)
	return res
}

// runTime checks an RFC 868 time server: 4 bytes, seconds since 1900.
func runTime(ctx context.Context, p udpPlan, o opts) Result {
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	maxOff, _ := o.durOpt("max-offset", 0)
	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	req := []byte{0}
	t1 := time.Now()
	resp, lat, err := u.exchange(ctx, req, retr, func(b []byte) bool { return len(b) == 4 })
	if err != nil {
		return udpFail(u, err, req, o, "")
	}
	secs := uint32(resp[0])<<24 | uint32(resp[1])<<16 | uint32(resp[2])<<8 | uint32(resp[3])
	t := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(secs) * time.Second)
	res := ok("server time %s", t.Format(time.RFC3339))
	res.Latency, res.Evidence = lat, newEvidence(u.peer, req, resp)
	res.Evidence.Decoded = []Field{{"time", t.Format(time.RFC3339)}}
	off := t.Sub(t1.Add(lat / 2)).Round(time.Second)
	res.Metrics = map[string]float64{"time.offset_ms": ms(off)}
	switch {
	case t.Year() < 2020 || t.Year() > 2100:
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "implausible time "+t.Format(time.RFC3339)
	case maxOff > 0 && (off > maxOff+time.Second || -off > maxOff+time.Second):
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("clock offset %s exceeds %s", off, maxOff)
	}
	return res
}
