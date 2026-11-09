package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

var dnsTypes = map[string]uint16{"A": 1, "NS": 2, "CNAME": 5, "SOA": 6, "PTR": 12, "MX": 15, "TXT": 16, "AAAA": 28, "SRV": 33, "CAA": 257}

var dnsRcodes = map[string]int{"NOERROR": 0, "FORMERR": 1, "SERVFAIL": 2, "NXDOMAIN": 3, "NOTIMP": 4, "REFUSED": 5}

func rcodeName(c int) string {
	for k, v := range dnsRcodes {
		if v == c {
			return k
		}
	}
	return "RCODE" + strconv.Itoa(c)
}

func typeName(t uint16) string {
	for k, v := range dnsTypes {
		if v == t {
			return k
		}
	}
	return "TYPE" + strconv.Itoa(int(t))
}

func validateDNS(o opts) error {
	if q := o.str("qtype", ""); q != "" {
		if strings.EqualFold(q, "ANY") {
			return errors.New("qtype ANY is not allowed (amplification); ask for a specific type")
		}
		if _, ok := dnsTypes[strings.ToUpper(q)]; !ok {
			return fmt.Errorf("unsupported qtype %q", q)
		}
	}
	if q := o.str("query", ""); q != "" {
		if _, err := encodeName(q); err != nil {
			return err
		}
	}
	for _, rc := range strings.Split(o.str("rcode", ""), ",") {
		if rc = strings.TrimSpace(strings.ToUpper(rc)); rc != "" {
			if _, ok := dnsRcodes[rc]; !ok {
				return fmt.Errorf("unknown rcode %q", rc)
			}
		}
	}
	for _, k := range []string{"aa", "recurse"} {
		if _, err := o.boolOpt(k, false); err != nil {
			return err
		}
	}
	_, err := o.intOpt("min-answers", 0, 0, 1000)
	return err
}

func encodeName(name string) ([]byte, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	var b []byte
	if name != "" {
		for _, l := range strings.Split(name, ".") {
			if l == "" || len(l) > 63 {
				return nil, fmt.Errorf("invalid DNS name %q", name)
			}
			b = append(b, byte(len(l)))
			b = append(b, l...)
		}
	}
	b = append(b, 0)
	if len(b) > 255 {
		return nil, fmt.Errorf("DNS name too long")
	}
	return b, nil
}

func dnsQuery(id uint16, name string, qtype uint16, rd bool) []byte {
	q := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(q[0:], id)
	if rd {
		q[2] = 0x01
	}
	binary.BigEndian.PutUint16(q[4:], 1)
	nb, _ := encodeName(name)
	q = append(q, nb...)
	q = binary.BigEndian.AppendUint16(q, qtype)
	return binary.BigEndian.AppendUint16(q, 1) // IN
}

type dnsRR struct {
	Name string
	Type uint16
	Text string
}

type dnsMsg struct {
	ID      uint16
	AA, TC  bool
	Rcode   int
	Answers []dnsRR
}

var errDNSFormat = errors.New("malformed DNS reply")

// readName decodes a possibly compressed name at off.
func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	end := -1
	for jumps := 0; ; {
		if off >= len(msg) {
			return "", 0, errDNSFormat
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			name := strings.Join(labels, ".") + "."
			return name, end, nil
		case l&0xC0 == 0xC0:
			if off+1 >= len(msg) || jumps > 32 {
				return "", 0, errDNSFormat
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
			jumps++
		default:
			if off+1+l > len(msg) {
				return "", 0, errDNSFormat
			}
			labels = append(labels, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

func parseDNS(msg []byte) (dnsMsg, error) {
	var m dnsMsg
	if len(msg) < 12 {
		return m, errDNSFormat
	}
	m.ID = binary.BigEndian.Uint16(msg)
	m.AA, m.TC = msg[2]&0x04 != 0, msg[2]&0x02 != 0
	m.Rcode = int(msg[3] & 0x0F)
	qd, an := int(binary.BigEndian.Uint16(msg[4:])), int(binary.BigEndian.Uint16(msg[6:]))
	off := 12
	for i := 0; i < qd; i++ {
		_, n, err := readName(msg, off)
		if err != nil || n+4 > len(msg) {
			return m, errDNSFormat
		}
		off = n + 4
	}
	for i := 0; i < an && i < 256; i++ {
		name, n, err := readName(msg, off)
		if err != nil || n+10 > len(msg) {
			return m, errDNSFormat
		}
		typ := binary.BigEndian.Uint16(msg[n:])
		rdlen := int(binary.BigEndian.Uint16(msg[n+8:]))
		rd := n + 10
		if rd+rdlen > len(msg) {
			return m, errDNSFormat
		}
		m.Answers = append(m.Answers, dnsRR{Name: name, Type: typ, Text: rdataText(msg, typ, rd, rdlen)})
		off = rd + rdlen
	}
	return m, nil
}

func rdataText(msg []byte, typ uint16, off, n int) string {
	data := msg[off : off+n]
	switch typ {
	case 1:
		if a, ok := netip.AddrFromSlice(data); ok && n == 4 {
			return a.String()
		}
	case 28:
		if a, ok := netip.AddrFromSlice(data); ok && n == 16 {
			return a.String()
		}
	case 2, 5, 12:
		if name, _, err := readName(msg, off); err == nil {
			return name
		}
	case 15:
		if n > 2 {
			if name, _, err := readName(msg, off+2); err == nil {
				return strconv.Itoa(int(binary.BigEndian.Uint16(data))) + " " + name
			}
		}
	case 16:
		var parts []string
		for i := 0; i < n; {
			l := int(data[i])
			if i+1+l > n {
				break
			}
			parts = append(parts, string(data[i+1:i+1+l]))
			i += 1 + l
		}
		return strings.Join(parts, "")
	case 6:
		mname, p1, err1 := readName(msg, off)
		rname, p2, err2 := readName(msg, p1)
		if err1 == nil && err2 == nil && p2+4 <= len(msg) {
			_ = p1
			return fmt.Sprintf("%s %s %d", mname, rname, binary.BigEndian.Uint32(msg[p2:]))
		}
	}
	return fmt.Sprintf("% x", data)
}

func runDNS(ctx context.Context, p udpPlan, o opts) Result {
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	content := o.has("query")
	name, qtype, rd := ".", dnsTypes["NS"], false
	if content {
		name = o.str("query", ".")
		qtype = dnsTypes[strings.ToUpper(o.str("qtype", "A"))]
		rd = o.str("recurse", "yes") != "no"
	}
	id := binary.BigEndian.Uint16(randBytes(2))
	req := dnsQuery(id, name, qtype, rd)
	resp, lat, err := u.exchange(ctx, req, retr, func(b []byte) bool {
		return len(b) >= 12 && binary.BigEndian.Uint16(b) == id && b[2]&0x80 != 0
	})
	if err != nil {
		return udpFail(u, err, req, o, "")
	}
	m, perr := parseDNS(resp)
	res := Result{Latency: lat, Evidence: newEvidence(u.peer, req, resp), Metrics: map[string]float64{"latency_ms": ms(lat)}}
	if perr != nil {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "malformed DNS reply"
		return res
	}
	res.Metrics["dns.answers"] = float64(len(m.Answers))
	dec := []Field{{"question", name + " " + typeName(qtype)}, {"rcode", rcodeName(m.Rcode)}, {"aa", strconv.FormatBool(m.AA)}}
	for i, a := range m.Answers {
		if i == 8 {
			break
		}
		dec = append(dec, Field{"answer", a.Name + " " + typeName(a.Type) + " " + a.Text})
	}
	res.Evidence.Decoded = dec
	if !content {
		switch rcodeName(m.Rcode) {
		case "NOERROR", "NXDOMAIN":
			res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, "DNS server answers ("+rcodeName(m.Rcode)+")"
		case "REFUSED":
			res.Vote, res.Reason, res.Detail = VoteOK, ReasonProtoReject, "DNS server answers (REFUSED: alive, not serving this query)"
		default:
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "DNS server replied "+rcodeName(m.Rcode)
		}
		return res
	}
	want := map[int]bool{}
	for _, rc := range strings.Split(o.str("rcode", "NOERROR"), ",") {
		if rc = strings.TrimSpace(strings.ToUpper(rc)); rc != "" {
			want[dnsRcodes[rc]] = true
		}
	}
	minAns := 0
	if len(want) == 1 && want[0] {
		minAns = 1
	}
	minAns, _ = o.intOpt("min-answers", minAns, 0, 1000)
	res.Vote, res.Reason = VoteFail, ReasonAssertFailed
	switch {
	case !want[m.Rcode]:
		res.Detail = fmt.Sprintf("%s %s: %s", name, typeName(qtype), rcodeName(m.Rcode))
		if m.Rcode == 2 {
			res.Reason = ReasonProtoError
		}
	case o.on("aa") && !m.AA:
		res.Detail = "the answer is not authoritative"
	case len(m.Answers) < minAns:
		res.Detail = fmt.Sprintf("%d answers, expected at least %d", len(m.Answers), minAns)
	default:
		exp := o.str("expect", "")
		matched := exp == ""
		var texts []string
		for _, a := range m.Answers {
			texts = append(texts, a.Text)
			if exp != "" && strings.Contains(a.Text, exp) {
				matched = true
			}
		}
		if !matched {
			res.Detail = fmt.Sprintf("no answer contains %q (got %s)", exp, strings.Join(texts, ", "))
			break
		}
		// expect-all: the answers are exactly this set, so an added or
		// replaced record (a hijack, a stale entry) fails.
		if all := o.str("expect-all", ""); all != "" {
			want := map[string]bool{}
			for _, v := range strings.Split(all, ",") {
				if v = strings.TrimSuffix(strings.TrimSpace(v), "."); v != "" {
					want[strings.ToLower(v)] = true
				}
			}
			got := map[string]bool{}
			for _, t := range texts {
				got[strings.ToLower(strings.TrimSuffix(t, "."))] = true
			}
			same := len(got) == len(want)
			for v := range want {
				same = same && got[v]
			}
			if !same {
				res.Detail = fmt.Sprintf("answers %s, expected exactly %s", strings.Join(texts, ", "), all)
				break
			}
		}
		res.Vote, res.Reason = VoteOK, ReasonOK
		res.Detail = fmt.Sprintf("%s %s: %s", name, typeName(qtype), strings.Join(texts, ", "))
	}
	return res
}
