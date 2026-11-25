package probe

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Hop is one router on the way to a destination.
type Hop struct {
	TTL  int           `json:"ttl"`
	Addr string        `json:"addr,omitempty"` // "" = no answer at this distance
	RTT  time.Duration `json:"-"`
	RTTs float64       `json:"rtt_ms"`
}

// ErrTraceNotAllowed means this process may not read the routers' answers:
// that takes a raw ICMP socket (root, or CAP_NET_RAW).
var ErrTraceNotAllowed = errors.New("tracing the route needs a raw ICMP socket (root or CAP_NET_RAW)")

// Trace sends ICMP echo requests with growing TTL towards dst (IPv4) and
// records which router answers at each distance, up to maxHops or the
// destination. Each distance waits at most per. Nothing outside this
// process runs: no traceroute command, no shell.
func Trace(ctx context.Context, dst netip.Addr, maxHops int, per time.Duration) ([]Hop, error) {
	if !dst.Is4() {
		return nil, errors.New("only IPv4 destinations are traced")
	}
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, ErrTraceNotAllowed
	}
	defer c.Close()
	pc := c.IPv4PacketConn()
	idb := make([]byte, 2)
	_, _ = rand.Read(idb)
	id := int(idb[0])<<8 | int(idb[1])
	buf := make([]byte, 1500)
	var hops []Hop
	for ttl := 1; ttl <= maxHops; ttl++ {
		if ctx.Err() != nil {
			break
		}
		if err := pc.SetTTL(ttl); err != nil {
			return hops, err
		}
		msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: id, Seq: ttl, Data: []byte("unagikuma-trace")}}
		b, _ := msg.Marshal(nil)
		start := time.Now()
		if _, err := c.WriteTo(b, &net.IPAddr{IP: dst.AsSlice()}); err != nil {
			return hops, err
		}
		h := Hop{TTL: ttl}
		deadline := start.Add(per)
		for time.Now().Before(deadline) {
			_ = c.SetReadDeadline(deadline)
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				break
			}
			m, err := icmp.ParseMessage(1, buf[:n])
			if err != nil {
				continue
			}
			ip, _ := netip.AddrFromSlice(from.(*net.IPAddr).IP)
			ok := false
			switch body := m.Body.(type) {
			case *icmp.TimeExceeded:
				ok = quotes(body.Data, id, ttl)
			case *icmp.Echo:
				ok = m.Type == ipv4.ICMPTypeEchoReply && body.ID == id && body.Seq == ttl
			}
			if ok {
				h.Addr, h.RTT = ip.Unmap().String(), time.Since(start)
				h.RTTs = ms(h.RTT)
				break
			}
		}
		hops = append(hops, h)
		if h.Addr == dst.String() {
			break
		}
	}
	return hops, nil
}

// quotes reports whether an ICMP error quotes our echo request (the IPv4
// header and the first 8 bytes of the ICMP message).
func quotes(data []byte, id, seq int) bool {
	if len(data) < 20 {
		return false
	}
	hl := int(data[0]&0x0f) * 4
	if len(data) < hl+8 || data[hl] != 8 {
		return false
	}
	return int(data[hl+4])<<8|int(data[hl+5]) == id && int(data[hl+6])<<8|int(data[hl+7]) == seq
}
