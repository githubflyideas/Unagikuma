package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"sort"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

var pingOpts = map[string]bool{"count": true, "interval": true, "loss": true, "rtt": true, "size": true, "fallback": true}

func validatePing(o opts) error {
	if err := rejectUnknown(o, pingOpts); err != nil {
		return err
	}
	if _, err := o.intOpt("count", 3, 1, 20); err != nil {
		return err
	}
	if _, err := o.intOpt("size", 32, 8, 1400); err != nil {
		return err
	}
	if _, err := o.durOpt("interval", 0); err != nil {
		return err
	}
	if _, err := o.durOpt("rtt", 0); err != nil {
		return err
	}
	if f := o.str("fallback", "tcp"); f != "tcp" && f != "off" {
		return fmt.Errorf("fallback=%s: use tcp or off", f)
	}
	_, err := o.percent("loss", 0)
	return err
}

// errNoICMP: neither unprivileged nor raw ICMP sockets are available.
var errNoICMP = errors.New("cannot open an ICMP socket")

// listenICMP prefers unprivileged ICMP sockets (Linux with
// net.ipv4.ping_group_range, macOS) and falls back to raw sockets.
func listenICMP(v6 bool) (*icmp.PacketConn, bool, error) {
	nets := [][2]string{{"udp4", "0.0.0.0"}, {"ip4:icmp", "0.0.0.0"}}
	if v6 {
		nets = [][2]string{{"udp6", "::"}, {"ip6:ipv6-icmp", "::"}}
	}
	var lastErr error
	for i, n := range nets {
		c, err := icmp.ListenPacket(n[0], n[1])
		if err == nil {
			return c, i == 0, nil
		}
		lastErr = err
	}
	if errors.Is(lastErr, syscall.EPERM) || errors.Is(lastErr, syscall.EACCES) || errors.Is(lastErr, syscall.EPROTONOSUPPORT) {
		return nil, false, errNoICMP
	}
	return nil, false, lastErr
}

func runPing(ctx context.Context, s Spec) Result {
	o := opts(s.Opts)
	count, _ := o.intOpt("count", 3, 1, 20)
	size, _ := o.intOpt("size", 32, 8, 1400)
	gap, _ := o.durOpt("interval", 200*time.Millisecond)
	lossMax, _ := o.percent("loss", 0)
	rttMax, _ := o.durOpt("rtt", 0)

	ip, err := resolve(ctx, s.Target, o)
	if err != nil {
		return classify(err, false, o)
	}
	v6 := !ip.Is4()
	conn, dgram, err := listenICMP(v6)
	if err != nil {
		if errors.Is(err, errNoICMP) {
			if o.str("fallback", "tcp") == "tcp" {
				return tcpReach(ctx, ip)
			}
			return abstain(ReasonPrivilege, privilegeHint)
		}
		return abstain(ReasonPlatform, "ICMP unavailable: %v", rootErr(err))
	}
	defer conn.Close()
	var dst net.Addr = &net.IPAddr{IP: ip.AsSlice()}
	if dgram {
		dst = &net.UDPAddr{IP: ip.AsSlice()}
	}
	proto, typ, reply := 1, icmp.Type(ipv4.ICMPTypeEcho), icmp.Type(ipv4.ICMPTypeEchoReply)
	if v6 {
		proto, typ, reply = 58, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}
	nonce := make([]byte, size)
	_, _ = rand.Read(nonce)
	id := int(nonce[0])<<8 | int(nonce[1])
	sent := map[int]time.Time{}
	var rtts []time.Duration
	var unreachable string
	deadline, _ := ctx.Deadline()
	readUntil := deadline
	buf := make([]byte, 1500)
	peer := netip.AddrPortFrom(ip, 0).Addr().String()
	var lastReq []byte

	for seq := 1; seq <= count; seq++ {
		payload := append([]byte(nil), nonce...)
		payload[2], payload[3] = byte(seq>>8), byte(seq)
		msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}
		b, err := msg.Marshal(nil)
		if err != nil {
			return abstain(ReasonInternal, "%v", err)
		}
		lastReq = b
		sent[seq] = time.Now()
		if _, err := conn.WriteTo(b, dst); err != nil {
			r := classify(err, false, o)
			r.Evidence = &Evidence{Peer: peer}
			return r
		}
		// Collect replies until the next packet is due (or the end).
		next := time.Now().Add(gap)
		if seq == count || next.After(deadline) {
			next = readUntil
		}
		for time.Now().Before(next) {
			_ = conn.SetReadDeadline(next)
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, os.ErrDeadlineExceeded) {
					break
				}
				return abstain(ReasonPlatform, "ICMP read failed: %v", rootErr(err))
			}
			m, err := icmp.ParseMessage(proto, buf[:n])
			if err != nil {
				continue
			}
			switch body := m.Body.(type) {
			case *icmp.Echo:
				if m.Type != reply || !sameHost(from, ip) || len(body.Data) < 4 || !bytes.Equal(body.Data[4:], nonce[4:]) {
					continue
				}
				q := int(body.Data[2])<<8 | int(body.Data[3])
				if t0, ok := sent[q]; ok {
					rtts = append(rtts, time.Since(t0))
					delete(sent, q)
				}
			case *icmp.DstUnreach:
				if len(body.Data) > 0 && bytes.Contains(body.Data, nonceTag(nonce)) {
					unreachable = fmt.Sprintf("ICMP destination unreachable (code %d) from %v", m.Code, from)
				}
			}
			if len(sent) == 0 && seq == count {
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	received := len(rtts)
	loss := 1 - float64(received)/float64(count)
	r := Result{Metrics: map[string]float64{"ping.loss": loss}, Evidence: newEvidence(peer, lastReq, nil)}
	if received > 0 {
		r.Metrics["ping.jitter_ms"] = jitter(rtts) // arrival order
		sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
		var sum time.Duration
		for _, d := range rtts {
			sum += d
		}
		avg := sum / time.Duration(received)
		r.Latency = avg
		r.Metrics["ping.rtt_min_ms"] = ms(rtts[0])
		r.Metrics["ping.rtt_avg_ms"] = ms(avg)
		r.Metrics["ping.rtt_max_ms"] = ms(rtts[received-1])
		r.Metrics["latency_ms"] = ms(avg)
	}
	switch {
	case received == 0 && unreachable != "":
		r.Vote, r.Reason, r.Detail = VoteFail, ReasonHostUnreachable, unreachable
	case received == 0:
		r.Vote, r.Reason, r.Detail = VoteFail, ReasonTimeout, fmt.Sprintf("no reply to %d echo requests", count)
	case lossMax > 0 && loss > lossMax:
		r.Vote, r.Reason, r.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("packet loss %.0f%% above %.0f%%", loss*100, lossMax*100)
	default:
		r.Vote, r.Reason = VoteOK, ReasonOK
		r.Detail = fmt.Sprintf("%d/%d replies, avg %.1f ms", received, count, r.Metrics["ping.rtt_avg_ms"])
		if rttMax > 0 && r.Latency > rttMax {
			r.Degraded = true
			r.Detail += fmt.Sprintf(" (above %s)", rttMax)
		}
		if loss > 0 && lossMax == 0 {
			r.Degraded = true
		}
	}
	return r
}

// nonceTag is the random part of a request that an ICMP error quotes back:
// up to 8 bytes after the ID and sequence, fewer when size= is smaller.
func nonceTag(nonce []byte) []byte {
	return nonce[4:min(len(nonce), 12)]
}

func sameHost(a net.Addr, ip netip.Addr) bool {
	var got net.IP
	switch x := a.(type) {
	case *net.IPAddr:
		got = x.IP
	case *net.UDPAddr:
		got = x.IP
	default:
		return false
	}
	g, ok := netip.AddrFromSlice(got)
	return ok && g.Unmap() == ip.Unmap()
}

// jitter is the mean absolute difference between consecutive RTTs in
// arrival order.
func jitter(rtts []time.Duration) float64 {
	if len(rtts) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(rtts); i++ {
		sum += math.Abs(ms(rtts[i]) - ms(rtts[i-1]))
	}
	return sum / float64(len(rtts)-1)
}

// privilegeHint says how to allow ping for this process.
const privilegeHint = "ping is not allowed for this process: allow unprivileged ping (sysctl net.ipv4.ping_group_range=\"0 2147483647\") or grant CAP_NET_RAW (setcap cap_net_raw+ep, or AmbientCapabilities=CAP_NET_RAW in the systemd unit)"

// tcpReachPorts are knocked when ICMP is not allowed: a host that accepts
// or refuses a connection on any of them is up.
var tcpReachPorts = []uint16{443, 80, 22}

// tcpReach checks that a host is up without ICMP (no ping permission, as
// on a stock Debian or Ubuntu): a connection accepted or refused (TCP RST)
// on a common port proves it answers. A host that says "unreachable" is
// down. When every port stays silent (a firewall drops them) nothing can
// be concluded: the check abstains with a hint rather than raising a false
// alarm.
func tcpReach(ctx context.Context, ip netip.Addr) Result {
	type out struct {
		port uint16
		err  error
		rtt  time.Duration
	}
	ch := make(chan out, len(tcpReachPorts))
	var d net.Dialer
	for _, p := range tcpReachPorts {
		go func(p uint16) {
			t0 := time.Now()
			c, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(ip, p).String())
			if err == nil {
				c.Close()
			}
			ch <- out{p, err, time.Since(t0)}
		}(p)
	}
	peer := ip.String()
	var unreachable error
	for range tcpReachPorts {
		o := <-ch
		if o.err == nil || isConnRefused(o.err) {
			r := Result{Vote: VoteOK, Reason: ReasonOK, Latency: o.rtt, Evidence: &Evidence{Peer: peer},
				Metrics: map[string]float64{"latency_ms": ms(o.rtt)}}
			r.Detail = fmt.Sprintf("answered on TCP port %d in %.1f ms (checked over TCP: ping is not allowed for this process)", o.port, ms(o.rtt))
			return r
		}
		if errors.Is(o.err, syscall.EHOSTUNREACH) || errors.Is(o.err, syscall.EHOSTDOWN) {
			unreachable = o.err
		}
		if errors.Is(o.err, syscall.ENETUNREACH) {
			return abstain(ReasonNoRoute, "no route from this host (%v)", rootErr(o.err))
		}
	}
	if unreachable != nil {
		r := fail(ReasonHostUnreachable, "host unreachable (checked over TCP: ping is not allowed for this process)")
		r.Evidence = &Evidence{Peer: peer}
		return r
	}
	return abstain(ReasonPrivilege, privilegeHint)
}
