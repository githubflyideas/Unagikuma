package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

var tcpOpts = map[string]bool{
	"send": true, "send-hex": true, "expect": true, "expect-hex": true, "expect-regex": true,
	"tls": true, "servername": true, "insecure": true, "cert-days": true, "cert-critical-days": true,
}

func validateTCP(target string, o opts) error {
	allowed := map[string]bool{}
	for k := range tcpOpts {
		allowed[k] = true
	}
	for k := range protoOpts {
		allowed[k] = true
	}
	if err := rejectUnknown(o, allowed); err != nil {
		return err
	}
	if err := validateProto(o); err != nil {
		return err
	}
	if _, _, err := splitHostPort(target); err != nil {
		return err
	}
	if o.has("send") && o.has("send-hex") {
		return errors.New("use send or send-hex, not both")
	}
	if v := o.str("send-hex", ""); v != "" {
		if _, err := decodeHex(v); err != nil {
			return fmt.Errorf("send-hex: %v", err)
		}
	}
	if v := o.str("expect-hex", ""); v != "" {
		if _, err := decodeHex(v); err != nil {
			return fmt.Errorf("expect-hex: %v", err)
		}
	}
	if _, err := regexpOpt(o, "expect-regex"); err != nil {
		return err
	}
	for _, k := range []string{"tls", "insecure"} {
		if _, err := o.boolOpt(k, false); err != nil {
			return err
		}
	}
	for _, k := range []string{"cert-days", "cert-critical-days"} {
		if _, err := o.intOpt(k, 0, 0, 3650); err != nil {
			return err
		}
	}
	return nil
}

// decodeHex accepts "de ad be ef", "deadbeef" and "de:ad".
func decodeHex(s string) ([]byte, error) {
	s = strings.NewReplacer(" ", "", ":", "", "0x", "").Replace(s)
	return hex.DecodeString(s)
}

// unescape interprets \r \n \t \\ \xHH in send/expect strings.
func unescape(s string) []byte {
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '0':
			b.WriteByte(0)
		case 'x':
			if i+2 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(v))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.Bytes()
}

func runTCP(ctx context.Context, s Spec) Result {
	o := opts(s.Opts)
	host, port, _ := splitHostPort(s.Target)
	ip, err := resolve(ctx, host, o)
	if err != nil {
		return classify(err, false, o)
	}
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		r := classify(err, false, o)
		r.Evidence = &Evidence{Peer: addr}
		return r
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	r := ok("TCP connect ok")
	r.Latency = time.Since(start)
	r.Metrics = map[string]float64{"latency_ms": ms(r.Latency)}
	r.Evidence = &Evidence{Peer: addr}

	if o.on("tls") {
		sn := o.str("servername", host)
		tc := tls.Client(conn, &tls.Config{ServerName: sn, InsecureSkipVerify: o.on("insecure"), NextProtos: tlsALPN(o)}) //nolint:gosec // opt-in
		if err := tc.HandshakeContext(ctx); err != nil {
			return failKeep(r, ReasonTLS, "TLS handshake failed: %v", rootErr(err))
		}
		cs := tc.ConnectionState()
		r.Detail = "TLS ok (" + tls.VersionName(cs.Version) + ")"
		if len(cs.PeerCertificates) > 0 {
			if res, done := certDays(r, o, cs.PeerCertificates[0], start); done {
				return res
			}
			r = certAnnotate(r, o, cs.PeerCertificates[0], start)
		}
		conn = tc
		r.Latency = time.Since(start)
	}

	if o.has("proto") {
		return runProto(ctx, conn, host, o, r, start)
	}

	var send []byte
	if v := o.str("send-hex", ""); v != "" {
		send, _ = decodeHex(v)
	} else if v := o.str("send", ""); v != "" {
		send = unescape(v)
	}
	expect, expectHex := o.str("expect", ""), o.str("expect-hex", "")
	re, _ := regexpOpt(o, "expect-regex")
	if len(send) == 0 && expect == "" && expectHex == "" && re == nil {
		return r
	}
	if len(send) > 0 {
		if _, err := conn.Write(send); err != nil {
			res := classify(err, false, o)
			res.Detail, res.Evidence, res.Latency = oneLine("send failed: "+rootErr(err).Error()), r.Evidence, r.Latency
			return res
		}
	}
	var want []byte
	if expectHex != "" {
		want, _ = decodeHex(expectHex)
	} else if expect != "" {
		want = unescape(expect)
	}
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 1024)
	matched := func() bool {
		if want != nil && !bytes.Contains(buf, want) {
			return false
		}
		return re == nil || re.Match(buf)
	}
	for !matched() && len(buf) < cap(buf) {
		n, err := conn.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	r.Evidence = newEvidence(addr, send, buf)
	r.Latency = time.Since(start)
	if (want != nil || re != nil) && !matched() {
		if len(buf) == 0 {
			return failKeep(r, ReasonTimeout, "connected, but no reply arrived")
		}
		return failKeep(r, ReasonAssertFailed, "reply does not contain the expected text")
	}
	r.Detail = "reply matched"
	return r
}
