package probe

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"
)

// ntpEpoch is 1900-01-01, the NTP era 0 origin.
var ntpEpoch = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)

func toNTP(t time.Time) uint64 {
	d := t.Sub(ntpEpoch)
	secs := uint64(d / time.Second)
	frac := uint64(d%time.Second) << 32 / uint64(time.Second)
	return secs<<32 | frac
}

func fromNTP(v uint64) time.Time {
	secs := v >> 32
	frac := (v & 0xFFFFFFFF) * uint64(time.Second) >> 32
	return ntpEpoch.Add(time.Duration(secs)*time.Second + time.Duration(frac))
}

func runNTP(ctx context.Context, p udpPlan, o opts) Result {
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	maxOff, _ := o.durOpt("max-offset", time.Second)
	maxStratum, _ := o.intOpt("max-stratum", 15, 1, 16)
	u, r, good := dialUDP(ctx, p, o)
	if !good {
		return r
	}
	defer u.Close()
	req := make([]byte, 48)
	req[0] = 0<<6 | 4<<3 | 3 // LI 0, version 4, client
	t1 := time.Now()
	// The low bits of the transmit timestamp are random: the reply must
	// echo them back (origin check), which defeats blind spoofing.
	xmt := toNTP(t1)&^0xFFFF | uint64(binary.BigEndian.Uint16(randBytes(2)))
	binary.BigEndian.PutUint64(req[40:], xmt)
	var t4 time.Time
	resp, lat, err := u.exchange(ctx, req, retr, func(b []byte) bool {
		if len(b) < 48 || b[0]&7 != 4 || binary.BigEndian.Uint64(b[24:]) != xmt {
			return false
		}
		t4 = time.Now()
		return true
	})
	if err != nil {
		return udpFail(u, err, req, o, "")
	}
	li, vn, stratum := resp[0]>>6, resp[0]>>3&7, int(resp[1])
	refid := resp[12:16]
	t2, t3 := fromNTP(binary.BigEndian.Uint64(resp[32:])), fromNTP(binary.BigEndian.Uint64(resp[40:]))
	offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
	delay := t4.Sub(t1) - t3.Sub(t2)
	res := Result{Latency: lat, Evidence: newEvidence(u.peer, req, resp)}
	ref := fmt.Sprintf("% x", refid)
	if stratum <= 1 {
		ref = string(bytes.TrimRight(refid, "\x00"))
	}
	res.Evidence.Decoded = []Field{{"li", fmt.Sprint(li)}, {"version", fmt.Sprint(vn)}, {"mode", "server"}, {"stratum", fmt.Sprint(stratum)}, {"refid", ref}, {"offset", offset.Round(time.Microsecond).String()}, {"delay", delay.Round(time.Microsecond).String()}}
	if stratum == 0 {
		code := string(bytes.TrimRight(refid, "\x00"))
		switch code {
		case "RATE":
			res.Vote, res.Reason, res.Degraded, res.Detail = VoteOK, ReasonRemoteRateLimited, true, "server asks us to slow down (kiss-o'-death RATE)"
		case "DENY", "RSTR":
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonAuthError, "server refuses this client (kiss-o'-death "+code+")"
		default:
			res.Vote, res.Reason, res.Detail = VoteFail, ReasonProtoError, "kiss-o'-death "+code
		}
		return res
	}
	res.Metrics = map[string]float64{"ntp.offset_ms": ms(offset), "ntp.stratum": float64(stratum), "ntp.delay_ms": ms(delay), "latency_ms": ms(lat)}
	switch {
	case stratum > maxStratum:
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("stratum %d is above %d (not synchronised)", stratum, maxStratum)
	case li == 3:
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, "server clock is not synchronised (leap indicator 3)"
	case maxOff > 0 && (offset > maxOff || -offset > maxOff):
		res.Vote, res.Reason, res.Detail = VoteFail, ReasonAssertFailed, fmt.Sprintf("offset %s exceeds %s", offset.Round(time.Millisecond), maxOff)
	default:
		res.Vote, res.Reason, res.Detail = VoteOK, ReasonOK, fmt.Sprintf("stratum %d, offset %s", stratum, offset.Round(time.Millisecond))
	}
	return res
}
