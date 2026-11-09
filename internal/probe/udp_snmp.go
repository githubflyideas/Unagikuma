package probe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Minimal BER for SNMP: enough to send GET / trap / v3 discovery and read
// the reply. Lengths and depths are bounded so a hostile agent cannot make
// the parser allocate or recurse without limit.

const (
	berInt       = 0x02
	berOctets    = 0x04
	berNull      = 0x05
	berOID       = 0x06
	berSeq       = 0x30
	snmpGet      = 0xA0
	snmpResponse = 0xA2
	snmpTrapV2   = 0xA7
	snmpReport   = 0xA8
)

func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte(n)}, b...)
		n >>= 8
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}

func tlv(tag byte, content ...[]byte) []byte {
	body := bytes.Join(content, nil)
	return append(append([]byte{tag}, berLen(len(body))...), body...)
}

func berInteger(v int64) []byte {
	var b []byte
	for {
		b = append([]byte{byte(v)}, b...)
		if (v >= -128 && v < 128) || len(b) == 8 {
			break
		}
		v >>= 8
	}
	return tlv(berInt, b)
}

func encodeOID(s string) ([]byte, error) {
	parts := strings.Split(strings.TrimPrefix(s, "."), ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid OID %q", s)
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid OID %q", s)
		}
		nums[i] = n
	}
	if nums[0] > 2 || (nums[0] < 2 && nums[1] > 39) {
		return nil, fmt.Errorf("invalid OID %q", s)
	}
	b := []byte{byte(nums[0]*40 + nums[1])}
	for _, n := range nums[2:] {
		var enc []byte
		enc = append(enc, byte(n&0x7F))
		for n >>= 7; n > 0; n >>= 7 {
			enc = append([]byte{byte(n&0x7F) | 0x80}, enc...)
		}
		b = append(b, enc...)
	}
	return tlv(berOID, b), nil
}

func decodeOID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	parts := []string{strconv.Itoa(int(b[0]) / 40), strconv.Itoa(int(b[0]) % 40)}
	var n uint64
	for _, c := range b[1:] {
		n = n<<7 | uint64(c&0x7F)
		if c&0x80 == 0 {
			parts = append(parts, strconv.FormatUint(n, 10))
			n = 0
		}
	}
	return strings.Join(parts, ".")
}

// berElem is one parsed TLV.
type berElem struct {
	tag  byte
	body []byte
}

var errBER = errors.New("malformed SNMP message")

func berRead(b []byte) (berElem, []byte, error) {
	if len(b) < 2 {
		return berElem{}, nil, errBER
	}
	tag, l, off := b[0], int(b[1]), 2
	if l&0x80 != 0 {
		n := l & 0x7F
		if n == 0 || n > 3 || len(b) < 2+n {
			return berElem{}, nil, errBER
		}
		l = 0
		for i := 0; i < n; i++ {
			l = l<<8 | int(b[2+i])
		}
		off = 2 + n
	}
	if off+l > len(b) {
		return berElem{}, nil, errBER
	}
	return berElem{tag, b[off : off+l]}, b[off+l:], nil
}

func berChildren(b []byte, max int) ([]berElem, error) {
	var out []berElem
	for len(b) > 0 {
		if len(out) >= max {
			return nil, errBER
		}
		e, rest, err := berRead(b)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		b = rest
	}
	return out, nil
}

func berInt64(b []byte) int64 {
	var v int64
	for i, c := range b {
		if i == 0 && c&0x80 != 0 {
			v = -1
		}
		v = v<<8 | int64(c)
	}
	return v
}

func berUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// snmpValue renders a varbind value; numeric is set for numeric types.
func snmpValue(e berElem) (text string, num float64, isNum bool, missing bool) {
	switch e.tag {
	case berInt:
		v := berInt64(e.body)
		return strconv.FormatInt(v, 10), float64(v), true, false
	case 0x41, 0x42, 0x46: // Counter32, Gauge32, Counter64
		v := berUint(e.body)
		return strconv.FormatUint(v, 10), float64(v), true, false
	case 0x43: // TimeTicks
		v := berUint(e.body)
		return (time.Duration(v) * 10 * time.Millisecond).String(), float64(v), true, false
	case berOctets:
		if printable(e.body) {
			return string(e.body), 0, false, false
		}
		return fmt.Sprintf("% x", e.body), 0, false, false
	case berOID:
		return decodeOID(e.body), 0, false, false
	case 0x40: // IpAddress
		if len(e.body) == 4 {
			return fmt.Sprintf("%d.%d.%d.%d", e.body[0], e.body[1], e.body[2], e.body[3]), 0, false, false
		}
	case berNull:
		return "null", 0, false, false
	case 0x80, 0x81, 0x82:
		return map[byte]string{0x80: "noSuchObject", 0x81: "noSuchInstance", 0x82: "endOfMibView"}[e.tag], 0, false, true
	}
	return fmt.Sprintf("% x", e.body), 0, false, false
}

func printable(b []byte) bool {
	for _, c := range b {
		if (c < 0x20 && c != '\n' && c != '\r' && c != '\t') || c == 0x7F {
			return false
		}
	}
	return true
}

func validateSNMP(o opts) error {
	switch o.str("version", "2c") {
	case "1", "2c", "3-discovery":
	default:
		return errors.New("version must be 1, 2c or 3-discovery")
	}
	if oid := o.str("oid", ""); oid != "" {
		if _, err := encodeOID(oid); err != nil {
			return err
		}
	}
	if v := o.str("expect-num", ""); v != "" {
		if _, err := parseNumExpect(v); err != nil {
			return err
		}
	}
	return nil
}

const sysUpTime = "1.3.6.1.2.1.1.3.0"

func runSNMP(ctx context.Context, p udpPlan, o opts) Result {
	if o.str("version", "2c") == "3-discovery" {
		return runSNMPv3Discovery(ctx, p, o)
	}
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	community := o.str("community", "public")
	oidStr := o.str("oid", sysUpTime)
	version := int64(1)
	if o.str("version", "2c") == "1" {
		version = 0
	}
	oid, _ := encodeOID(oidStr)
	reqID := int64(binary.BigEndian.Uint32(randBytes(4)) & 0x7FFFFFFF)
	pdu := tlv(snmpGet, berInteger(reqID), berInteger(0), berInteger(0), tlv(berSeq, tlv(berSeq, oid, tlv(berNull))))
	req := tlv(berSeq, berInteger(version), tlv(berOctets, []byte(community)), pdu)

	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	var vb []berElem
	var errStatus int64
	resp, lat, err := u.exchange(ctx, req, retr, func(b []byte) bool {
		msg, _, err := berRead(b)
		if err != nil || msg.tag != berSeq {
			return false
		}
		parts, err := berChildren(msg.body, 3)
		if err != nil || len(parts) != 3 || parts[2].tag != snmpResponse {
			return false
		}
		f, err := berChildren(parts[2].body, 4)
		if err != nil || len(f) != 4 || berInt64(f[0].body) != reqID {
			return false
		}
		errStatus = berInt64(f[1].body)
		list, err := berChildren(f[3].body, 64)
		if err != nil {
			return false
		}
		vb = list
		return true
	})
	if err != nil {
		return udpFail(u, err, maskCommunity(req, community), o, "check the community string and the agent's ACL")
	}
	res := Result{Latency: lat, Evidence: newEvidence(u.peer, maskCommunity(req, community), maskCommunity(resp, community))}
	dec := []Field{{"version", map[int64]string{0: "1", 1: "2c"}[version]}, {"community", strings.Repeat("*", len(community))}, {"pdu", "GetResponse"}, {"error-status", strconv.FormatInt(errStatus, 10)}}
	if errStatus != 0 {
		res.Evidence.Decoded = dec
		reason := ReasonProtoError
		if errStatus == 2 { // noSuchName (v1)
			reason = ReasonAssertFailed
		}
		res.Vote, res.Reason, res.Detail = VoteFail, reason, fmt.Sprintf("agent answered with error-status %d", errStatus)
		return res
	}
	if len(vb) == 0 {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "empty varbind list"
		return res
	}
	pair, err := berChildren(vb[0].body, 2)
	if err != nil || len(pair) != 2 {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "malformed varbind"
		return res
	}
	text, num, isNum, missing := snmpValue(pair[1])
	dec = append(dec, Field{decodeOID(pair[0].body), text})
	res.Evidence.Decoded = dec
	res.Metrics = map[string]float64{"latency_ms": ms(lat)}
	if isNum {
		res.Metrics["snmp.value"] = num
	}
	res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, oidStr+" = "+text
	switch exp, expNum := o.str("expect", ""), o.str("expect-num", ""); {
	case missing:
		res.Vote, res.Reason = VoteFail, ReasonAssertFailed
	case exp != "" && !strings.Contains(text, exp):
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("%s = %q does not contain %q", oidStr, text, exp)
	case expNum != "":
		ne, _ := parseNumExpect(expNum)
		if !isNum || !ne.match(num) {
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("%s = %s, expected %s", oidStr, text, ne)
		}
	}
	return res
}

// runSNMPv3Discovery asks for the agent's engine ID with an empty user:
// any SNMPv3 agent answers with a Report, which proves it is alive
// without needing (or exposing) credentials.
func runSNMPv3Discovery(ctx context.Context, p udpPlan, o opts) Result {
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	msgID := int64(binary.BigEndian.Uint32(randBytes(4)) & 0x7FFFFFFF)
	header := tlv(berSeq, berInteger(msgID), berInteger(65507), tlv(berOctets, []byte{0x04}), berInteger(3))
	usm := tlv(berSeq, tlv(berOctets), berInteger(0), berInteger(0), tlv(berOctets), tlv(berOctets), tlv(berOctets))
	scoped := tlv(berSeq, tlv(berOctets), tlv(berOctets), tlv(snmpGet, berInteger(msgID), berInteger(0), berInteger(0), tlv(berSeq)))
	req := tlv(berSeq, berInteger(3), header, tlv(berOctets, usm), scoped)

	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	var engineID []byte
	var boots, etime int64
	var pduTag byte
	resp, lat, err := u.exchange(ctx, req, retr, func(b []byte) bool {
		msg, _, err := berRead(b)
		if err != nil || msg.tag != berSeq {
			return false
		}
		parts, err := berChildren(msg.body, 4)
		if err != nil || len(parts) != 4 || berInt64(parts[0].body) != 3 {
			return false
		}
		hdr, err := berChildren(parts[1].body, 4)
		if err != nil || len(hdr) != 4 || berInt64(hdr[0].body) != msgID {
			return false
		}
		sec, _, err := berRead(parts[2].body)
		if err != nil {
			return false
		}
		f, err := berChildren(sec.body, 6)
		if err != nil || len(f) != 6 {
			return false
		}
		engineID, boots, etime = f[0].body, berInt64(f[1].body), berInt64(f[2].body)
		sp, err := berChildren(parts[3].body, 3)
		if err == nil && len(sp) == 3 {
			pduTag = sp[2].tag
		}
		return true
	})
	if err != nil {
		return udpFail(u, err, req, o, "")
	}
	res := Result{Latency: lat, Evidence: newEvidence(u.peer, req, resp), Metrics: map[string]float64{"latency_ms": ms(lat)}}
	res.Evidence.Decoded = []Field{{"version", "3"}, {"engine-id", fmt.Sprintf("%x", engineID)}, {"engine-boots", strconv.FormatInt(boots, 10)}, {"engine-time", strconv.FormatInt(etime, 10)}}
	if len(engineID) == 0 || (pduTag != snmpReport && pduTag != snmpResponse) {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "SNMPv3 reply without an engine ID"
		return res
	}
	res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, fmt.Sprintf("SNMPv3 engine %x (boots %d)", engineID, boots)
	return res
}

// snmpTrap builds an SNMPv2c trap tagged as a unagikuma probe.
func snmpTrap(community string) []byte {
	upOID, _ := encodeOID(sysUpTime)
	trapOID, _ := encodeOID("1.3.6.1.6.3.1.1.4.1.0")
	marker, _ := encodeOID("1.3.6.1.3.1")
	descr, _ := encodeOID("1.3.6.1.2.1.1.1.0")
	vbs := tlv(berSeq,
		tlv(berSeq, upOID, tlv(0x43, []byte{0})),
		tlv(berSeq, trapOID, marker),
		tlv(berSeq, descr, tlv(berOctets, []byte("unagikuma-probe"))))
	reqID := int64(binary.BigEndian.Uint32(randBytes(4)) & 0x7FFFFFFF)
	return tlv(berSeq, berInteger(1), tlv(berOctets, []byte(community)), tlv(snmpTrapV2, berInteger(reqID), berInteger(0), berInteger(0), vbs))
}

// maskCommunity replaces the community string in evidence.
func maskCommunity(b []byte, community string) []byte {
	if community == "" {
		return b
	}
	return bytes.ReplaceAll(b, []byte(community), bytes.Repeat([]byte("*"), len(community)))
}
