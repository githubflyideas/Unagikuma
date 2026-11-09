package probe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- TFTP ----

// runTFTP requests a file. The server answers from a new port, so the
// socket is unconnected and accepts replies from the target's address on
// any port. DATA proves the server works; an ERROR (file not found...)
// still proves it is alive unless expect=data.
func runTFTP(ctx context.Context, p udpPlan, o opts) Result {
	file := o.str("file", "unagikuma-probe-"+hex.EncodeToString(randBytes(4)))
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	ip, err := resolve(ctx, p.host, o)
	if err != nil {
		return classify(err, true, o)
	}
	c, err := net.ListenUDP("udp", nil)
	if err != nil {
		return abstain(ReasonInternal, "cannot open a UDP socket: %v", err)
	}
	defer c.Close()
	dst := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(p.port)))
	req := []byte{0, 1}
	req = append(append(append(req, file...), 0), "octet"...)
	req = append(req, 0)
	resp, from, lat, err := unconnectedExchange(ctx, c, dst, req, retr, func(b []byte, from netip.AddrPort) bool {
		return from.Addr().Unmap() == ip.Unmap() && len(b) >= 4 && (b[1] == 3 || b[1] == 5) && b[0] == 0
	})
	if err != nil {
		r := classify(err, true, o)
		r.Evidence = newEvidence(dst.String(), req, nil)
		return r
	}
	res := Result{Latency: lat, Evidence: newEvidence(dst.String(), req, resp)}
	switch resp[1] {
	case 3: // DATA: stop the transfer politely
		_, _ = c.WriteToUDPAddrPort(append([]byte{0, 5, 0, 0}, "unagikuma probe done\x00"...), from)
		res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, fmt.Sprintf("%s: received data", file)
		res.Evidence.Decoded = []Field{{"opcode", "DATA"}, {"block", strconv.Itoa(int(binary.BigEndian.Uint16(resp[2:])))}}
	case 5:
		code := binary.BigEndian.Uint16(resp[2:])
		msg := strings.TrimRight(string(resp[4:]), "\x00")
		res.Evidence.Decoded = []Field{{"opcode", "ERROR"}, {"code", strconv.Itoa(int(code))}, {"message", msg}}
		if o.str("expect", "any") == "data" {
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("%s: %s", file, msg)
		} else {
			res.Vote, res.Reason, res.Detail = VoteOK, ReasonProtoReject, fmt.Sprintf("server alive (error %d: %s)", code, msg)
		}
	}
	return res
}

// unconnectedExchange sends from an unconnected socket and accepts replies
// that satisfy accept (source address checks happen there).
func unconnectedExchange(ctx context.Context, c *net.UDPConn, dst *net.UDPAddr, req []byte, retransmit int, accept func([]byte, netip.AddrPort) bool) ([]byte, netip.AddrPort, time.Duration, error) {
	deadline, _ := ctx.Deadline()
	slot := time.Until(deadline) / time.Duration(retransmit+1)
	buf := make([]byte, 65535)
	start := time.Now()
	for attempt := 0; attempt <= retransmit; attempt++ {
		sentAt := time.Now()
		if _, err := c.WriteToUDP(req, dst); err != nil {
			return nil, netip.AddrPort{}, 0, err
		}
		until := sentAt.Add(slot)
		if attempt == retransmit || until.After(deadline) {
			until = deadline
		}
		for {
			_ = c.SetReadDeadline(until)
			n, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				if isTimeout(err) || errors.Is(err, os.ErrDeadlineExceeded) {
					break
				}
				if isConnRefused(err) {
					return nil, netip.AddrPort{}, 0, err
				}
				continue
			}
			if accept(buf[:n], from) {
				return append([]byte(nil), buf[:n]...), from, replyLatency(attempt, start, sentAt), nil
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, netip.AddrPort{}, 0, context.DeadlineExceeded
}

// ---- ONC RPC portmapper ----

var rpcPrograms = map[string][2]uint32{
	"portmapper": {100000, 2}, "rpcbind": {100000, 2}, "nfs": {100003, 3}, "mountd": {100005, 3},
	"nlockmgr": {100021, 4}, "status": {100024, 1}, "rquotad": {100011, 1},
}

func rpcProgram(s string) (prog, vers uint32, ok bool) {
	if v, found := rpcPrograms[strings.ToLower(s)]; found {
		return v[0], v[1], true
	}
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), 1, err == nil
}

func rpcCall(xid, prog, vers, proc uint32, args ...uint32) []byte {
	b := make([]byte, 0, 56)
	for _, v := range append([]uint32{xid, 0, 2, prog, vers, proc, 0, 0, 0, 0}, args...) {
		b = binary.BigEndian.AppendUint32(b, v)
	}
	return b
}

// runSunRPC calls the portmapper's NULL procedure, or GETPORT for
// expect-program to check that a service is registered.
func runSunRPC(ctx context.Context, p udpPlan, o opts) Result {
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	xid := binary.BigEndian.Uint32(randBytes(4))
	want := o.str("expect-program", "")
	req := rpcCall(xid, 100000, 2, 0)
	var prog, vers uint32
	if want != "" {
		prog, vers, _ = rpcProgram(want)
		req = rpcCall(xid, 100000, 2, 3, prog, vers, 17, 0)
	}
	resp, lat, err := u.exchange(ctx, req, retr, func(b []byte) bool {
		return len(b) >= 24 && binary.BigEndian.Uint32(b) == xid && binary.BigEndian.Uint32(b[4:]) == 1
	})
	if err != nil {
		return udpFail(u, err, req, o, "")
	}
	res := Result{Latency: lat, Evidence: newEvidence(u.peer, req, resp)}
	replyStat := binary.BigEndian.Uint32(resp[8:])
	if replyStat != 0 {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAuthError, "RPC call denied"
		return res
	}
	verfLen := binary.BigEndian.Uint32(resp[16:])
	off := 20 + int((verfLen+3)&^3)
	if verfLen > 400 || off+4 > len(resp) {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "malformed RPC reply"
		return res
	}
	if st := binary.BigEndian.Uint32(resp[off:]); st != 0 {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, fmt.Sprintf("RPC accept status %d", st)
		return res
	}
	res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, "portmapper answers"
	if want != "" {
		if off+8 > len(resp) {
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "malformed GETPORT reply"
			return res
		}
		port := binary.BigEndian.Uint32(resp[off+4:])
		res.Evidence.Decoded = []Field{{"program", fmt.Sprintf("%s (%d v%d)", want, prog, vers)}, {"port", strconv.Itoa(int(port))}}
		if port == 0 {
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, want+" is not registered with the portmapper"
		} else {
			res.Detail = fmt.Sprintf("%s registered on port %d", want, port)
		}
	}
	return res
}

// ---- raw UDP ----

func validateRaw(o opts) error {
	if o.has("send") && o.has("send-hex") {
		return errors.New("use send or send-hex, not both")
	}
	if v := o.str("send-hex", ""); v != "" {
		if _, err := decodeHex(expandNonce(v, nil)); err != nil {
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
	if _, err := o.intOpt("expect-len", 0, 1, 65535); err != nil {
		return err
	}
	if _, err := o.intOpt("src-port", 0, 1, 65535); err != nil {
		return err
	}
	switch o.str("accept-from", "connected") {
	case "connected", "same-ip", "any-port":
	default:
		return errors.New("accept-from must be connected, same-ip or any-port")
	}
	return nil
}

// expandNonce replaces {{nonce4}} / {{nonce8}} with random hex bytes
// (recorded in got, nil = validation only).
func expandNonce(s string, got *[]byte) string {
	for _, n := range []int{4, 8} {
		ph := "{{nonce" + strconv.Itoa(n) + "}}"
		for strings.Contains(s, ph) {
			b := randBytes(n)
			if got != nil {
				*got = append(*got, b...)
			}
			s = strings.Replace(s, ph, hex.EncodeToString(b), 1)
		}
	}
	return s
}

func runRaw(ctx context.Context, p udpPlan, o opts) Result {
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	var payload []byte
	if v := o.str("send-hex", ""); v != "" {
		payload, _ = decodeHex(expandNonce(v, nil))
	} else if v := o.str("send", ""); v != "" {
		payload = unescape(expandNonce(v, nil))
	}
	if len(payload) == 0 {
		payload = []byte("unagikuma-probe\n")
	}
	if rawDepth(o) == "L1" && !o.has("src-port") && o.str("accept-from", "connected") == "connected" {
		return runL1(ctx, p, o, payload)
	}
	var want []byte
	if v := o.str("expect-hex", ""); v != "" {
		want, _ = decodeHex(v)
	} else if v := o.str("expect", ""); v != "" && v != "any" && v != "none" {
		want = unescape(v)
	}
	re, _ := regexpOpt(o, "expect-regex")
	wantLen, _ := o.intOpt("expect-len", 0, 1, 65535)
	check := func(b []byte) (string, bool) {
		switch {
		case want != nil && !bytes.Contains(b, want):
			return "reply does not contain the expected bytes", false
		case re != nil && !re.Match(b):
			return "reply does not match " + re.String(), false
		case wantLen > 0 && len(b) != wantLen:
			return fmt.Sprintf("reply is %d bytes, expected %d", len(b), wantLen), false
		}
		return "", true
	}
	ip, err := resolve(ctx, p.host, o)
	if err != nil {
		return classify(err, true, o)
	}
	dst := net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(p.port)))
	src, _ := o.intOpt("src-port", 0, 1, 65535)
	var laddr *net.UDPAddr
	if src > 0 {
		laddr = &net.UDPAddr{Port: src}
	}
	if src > 0 {
		// Two checks cannot use one source port at once: they take turns.
		release, err := lockSourcePort(ctx, src)
		if err != nil {
			return abstain(ReasonPortBusy, "source port %d stayed in use by another check on this node", src)
		}
		defer release()
	}
	mode := o.str("accept-from", "connected")
	var resp []byte
	var lat time.Duration
	if mode == "connected" {
		c, err := net.DialUDP("udp", laddr, dst)
		if err != nil {
			if isPermission(err) {
				return abstain(ReasonPrivilege, "cannot bind source port %d: %v", src, rootErr(err))
			}
			if src > 0 && isAddrInUse(err) {
				return abstain(ReasonPortBusy, "source port %d is used by another program on this node", src)
			}
			return classify(err, true, o)
		}
		u := &udpSock{c: c, peer: dst.String(), errsOK: enableUDPErrors(c)}
		defer u.Close()
		resp, lat, err = u.exchange(ctx, payload, retr, nil)
		if err != nil {
			return udpFail(u, err, payload, o, "")
		}
	} else {
		c, err := net.ListenUDP("udp", laddr)
		if err != nil {
			if isPermission(err) {
				return abstain(ReasonPrivilege, "cannot bind source port %d: %v", src, rootErr(err))
			}
			if src > 0 && isAddrInUse(err) {
				return abstain(ReasonPortBusy, "source port %d is used by another program on this node", src)
			}
			return abstain(ReasonInternal, "cannot open a UDP socket: %v", err)
		}
		defer c.Close()
		resp, _, lat, err = unconnectedExchange(ctx, c, dst, payload, retr, func(b []byte, from netip.AddrPort) bool {
			return from.Addr().Unmap() == ip.Unmap() && (mode == "any-port" || from.Port() == uint16(p.port))
		})
		if err != nil {
			r := classify(err, true, o)
			r.Evidence = newEvidence(dst.String(), payload, nil)
			return r
		}
	}
	res := Result{Latency: lat, Evidence: newEvidence(dst.String(), payload, resp), Metrics: map[string]float64{"latency_ms": ms(lat)}}
	if msg, good := check(resp); !good {
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, msg
		return res
	}
	res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, fmt.Sprintf("reply of %d bytes", len(resp))
	return res
}

// sourcePorts holds one token per fixed source port in use by a check.
var sourcePorts sync.Map // int -> chan struct{}

// lockSourcePort waits for a fixed source port to be free of other checks
// in this process. A check that waited away more than half of its time
// gives up rather than time out on the target for a wait of its own.
func lockSourcePort(ctx context.Context, port int) (func(), error) {
	v, _ := sourcePorts.LoadOrStore(port, make(chan struct{}, 1))
	ch := v.(chan struct{})
	start := time.Now()
	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-ch }
	if deadline, ok := ctx.Deadline(); ok && time.Since(start) > 0 && time.Until(deadline) < deadline.Sub(start)/2 {
		release()
		return nil, context.DeadlineExceeded
	}
	return release, nil
}

func isPermission(err error) bool {
	return errors.Is(err, os.ErrPermission)
}
