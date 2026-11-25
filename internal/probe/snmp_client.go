package probe

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

// An SNMP client for checks that read several values per round: GET of a
// few OIDs at once, and GETNEXT for finding a port or a sensor by name.
// It speaks v1, v2c and v3 with the user-based security model: MD5, SHA-1,
// SHA-256 or SHA-512 authentication and DES or AES-128 privacy (RFC 3414,
// 3826, 7860). Only GET and GETNEXT are sent: nothing on the device is
// changed and nothing is walked in bulk.

const (
	snmpGetNext = 0xA1
	maxVarbinds = 32
)

// snmpVarbind is one OID and its value as received.
type snmpVarbind struct {
	OID   string
	Value berElem
}

// snmpAuthFailed is a v3 refusal: wrong user, password or level.
type snmpAuthFailed struct{ why string }

func (e snmpAuthFailed) Error() string { return e.why }

type snmpClient struct {
	u         *udpSock
	retr      int
	version   int64 // 0 = v1, 1 = v2c, 3
	community string
	v3        *usmUser
	// For evidence: the first exchange.
	req, resp []byte
	lat       time.Duration
	exchanges int
}

// usmUser is a v3 user with its keys localized to the agent's engine.
type usmUser struct {
	name               string
	authProto, privPro string // "" when the level does not use them
	authPass, privPass string
	authKey, privKey   []byte
	engineID           []byte
	boots, etime       int64
	timeAt             time.Time
	salt               uint64
}

func newSNMPClient(ctx context.Context, p udpPlan, o opts) (*snmpClient, Result, bool) {
	u, r, ok := dialUDP(ctx, p, o)
	if !ok {
		return nil, r, false
	}
	retr, _ := o.intOpt("retransmit", 1, 0, 5)
	c := &snmpClient{u: u, retr: retr, community: o.str("community", "public")}
	switch o.str("version", "2c") {
	case "1":
		c.version = 0
	case "3":
		c.version = 3
		c.community = ""
		c.v3 = &usmUser{name: o.str("user", ""), authProto: strings.ToLower(o.str("auth", "")), privPro: strings.ToLower(o.str("priv", "")),
			authPass: o.str("auth-pass", ""), privPass: o.str("priv-pass", "")}
	default:
		c.version = 1
	}
	return c, Result{}, true
}

func (c *snmpClient) Close() { c.u.Close() }

// masked is what evidence shows of a message: no community string.
func (c *snmpClient) masked(b []byte) []byte { return maskCommunity(b, c.community) }

// get reads the given OIDs in one request.
func (c *snmpClient) get(ctx context.Context, oids ...string) ([]snmpVarbind, error) {
	return c.request(ctx, snmpGet, oids)
}

// next returns the OID following oid and its value (one GETNEXT).
func (c *snmpClient) next(ctx context.Context, oid string) (snmpVarbind, error) {
	vbs, err := c.request(ctx, snmpGetNext, []string{oid})
	if err != nil {
		return snmpVarbind{}, err
	}
	if len(vbs) != 1 {
		return snmpVarbind{}, errors.New("GETNEXT answered with no value")
	}
	return vbs[0], nil
}

func (c *snmpClient) request(ctx context.Context, tag byte, oids []string) ([]snmpVarbind, error) {
	if len(oids) == 0 || len(oids) > maxVarbinds {
		return nil, fmt.Errorf("asks for %d values", len(oids))
	}
	var list [][]byte
	for _, s := range oids {
		oid, err := encodeOID(s)
		if err != nil {
			return nil, err
		}
		list = append(list, tlv(berSeq, oid, tlv(berNull)))
	}
	reqID := int64(binary.BigEndian.Uint32(randBytes(4)) & 0x7FFFFFFF)
	pdu := tlv(tag, berInteger(reqID), berInteger(0), berInteger(0), tlv(berSeq, list...))
	if c.version == 3 {
		return c.requestV3(ctx, pdu, reqID, false)
	}
	msg := tlv(berSeq, berInteger(c.version), tlv(berOctets, []byte(c.community)), pdu)
	var out []snmpVarbind
	var perr error
	resp, err := c.exchange(ctx, msg, func(b []byte) bool {
		m, _, err := berRead(b)
		if err != nil || m.tag != berSeq {
			return false
		}
		parts, err := berChildren(m.body, 3)
		if err != nil || len(parts) != 3 || parts[2].tag != snmpResponse {
			return false
		}
		out, perr = readPDU(parts[2], reqID)
		return perr != errNotOurs
	})
	if err != nil {
		return nil, err
	}
	_ = resp
	return out, perr
}

var errNotOurs = errors.New("not our request")

// readPDU reads a response PDU: the varbinds, or the agent's error.
func readPDU(p berElem, reqID int64) ([]snmpVarbind, error) {
	f, err := berChildren(p.body, 4)
	if err != nil || len(f) != 4 {
		return nil, errNotOurs
	}
	if berInt64(f[0].body) != reqID {
		return nil, errNotOurs
	}
	if st := berInt64(f[1].body); st != 0 {
		return nil, fmt.Errorf("agent answered with error-status %d (%s)", st, snmpErrors[st])
	}
	vbs, err := berChildren(f[3].body, maxVarbinds)
	if err != nil {
		return nil, fmt.Errorf("malformed varbinds: %w", err)
	}
	out := make([]snmpVarbind, 0, len(vbs))
	for _, vb := range vbs {
		pair, err := berChildren(vb.body, 2)
		if err != nil || len(pair) != 2 || pair[0].tag != berOID {
			return nil, errors.New("malformed varbind")
		}
		out = append(out, snmpVarbind{OID: decodeOID(pair[0].body), Value: pair[1]})
	}
	return out, nil
}

var snmpErrors = map[int64]string{1: "tooBig", 2: "noSuchName", 3: "badValue", 4: "readOnly", 5: "genErr", 6: "noAccess", 16: "authorizationError"}

func (c *snmpClient) exchange(ctx context.Context, msg []byte, accept func([]byte) bool) ([]byte, error) {
	resp, lat, err := c.u.exchange(ctx, msg, c.retr, accept)
	if c.exchanges == 0 {
		c.req, c.resp, c.lat = msg, resp, lat
	}
	c.exchanges++
	return resp, err
}

// ---- v3 ----

const (
	flagAuth       = 0x01
	flagPriv       = 0x02
	flagReportable = 0x04
)

// The agent's reasons for refusing a v3 request (usmStats*).
var usmReports = map[string]string{
	"1.3.6.1.6.3.15.1.1.1.0": "the agent does not support this security level",
	"1.3.6.1.6.3.15.1.1.2.0": "not in the agent's time window",
	"1.3.6.1.6.3.15.1.1.3.0": "unknown user name",
	"1.3.6.1.6.3.15.1.1.4.0": "unknown engine ID",
	"1.3.6.1.6.3.15.1.1.5.0": "wrong authentication password (digest mismatch)",
	"1.3.6.1.6.3.15.1.1.6.0": "wrong privacy password (cannot decrypt)",
}

func (c *snmpClient) requestV3(ctx context.Context, pdu []byte, reqID int64, retried bool) ([]snmpVarbind, error) {
	usr := c.v3
	if usr.engineID == nil {
		if err := c.discover(ctx); err != nil {
			return nil, err
		}
	}
	flags := byte(flagReportable)
	if usr.authProto != "" {
		flags |= flagAuth
		if usr.privPro != "" {
			flags |= flagPriv
		}
	}
	msgID := int64(binary.BigEndian.Uint32(randBytes(4)) & 0x7FFFFFFF)
	header := tlv(berSeq, berInteger(msgID), berInteger(65507), tlv(berOctets, []byte{flags}), berInteger(3))
	boots, etime := usr.boots, usr.etime+int64(time.Since(usr.timeAt).Seconds())
	scoped := tlv(berSeq, tlv(berOctets, usr.engineID), tlv(berOctets), pdu)
	var privParams []byte
	var payload []byte
	if flags&flagPriv != 0 {
		enc, salt, err := usr.encrypt(scoped, boots, etime)
		if err != nil {
			return nil, err
		}
		privParams, payload = salt, tlv(berOctets, enc)
	} else {
		payload = scoped
	}
	macLen := authLen(usr.authProto)
	placeholder := make([]byte, macLen)
	usm := tlv(berSeq, tlv(berOctets, usr.engineID), berInteger(boots), berInteger(etime), tlv(berOctets, []byte(usr.name)),
		tlv(berOctets, placeholder), tlv(berOctets, privParams))
	msg := tlv(berSeq, berInteger(3), header, tlv(berOctets, usm), payload)
	if flags&flagAuth != 0 {
		mac := usr.mac(msg)
		// The digest goes where the zeros were: the first such run is
		// ours, inside the security parameters.
		if i := bytes.Index(msg, append([]byte{berOctets, byte(macLen)}, placeholder...)); i >= 0 {
			copy(msg[i+2:], mac)
		}
	}
	var out []snmpVarbind
	var perr error
	_, err := c.exchange(ctx, msg, func(b []byte) bool {
		r, ok := c.readV3(b, msgID)
		if !ok {
			return false
		}
		if r.report != "" {
			perr = r.reportErr()
			return true
		}
		out, perr = readPDU(r.pdu, reqID)
		return perr != errNotOurs
	})
	if err != nil {
		return nil, err
	}
	var rep snmpAuthFailed
	if errors.As(perr, &rep) && strings.Contains(rep.why, "time window") && !retried {
		return c.requestV3(ctx, pdu, reqID, true) // the report carried the agent's clock
	}
	return out, perr
}

type v3Reply struct {
	pdu    berElem
	report string // OID of a usmStats report
}

func (r v3Reply) reportErr() error {
	if why, ok := usmReports[r.report]; ok {
		return snmpAuthFailed{why}
	}
	return fmt.Errorf("agent reported %s", r.report)
}

// readV3 checks and opens a v3 reply to msgID: the digest when the reply
// is authenticated, then the decryption.
func (c *snmpClient) readV3(b []byte, msgID int64) (v3Reply, bool) {
	usr := c.v3
	m, _, err := berRead(b)
	if err != nil || m.tag != berSeq {
		return v3Reply{}, false
	}
	parts, err := berChildren(m.body, 4)
	if err != nil || len(parts) != 4 || berInt64(parts[0].body) != 3 {
		return v3Reply{}, false
	}
	hdr, err := berChildren(parts[1].body, 4)
	if err != nil || len(hdr) != 4 || berInt64(hdr[0].body) != msgID || len(hdr[2].body) != 1 {
		return v3Reply{}, false
	}
	flags := hdr[2].body[0]
	sec, _, err := berRead(parts[2].body)
	if err != nil {
		return v3Reply{}, false
	}
	f, err := berChildren(sec.body, 6)
	if err != nil || len(f) != 6 {
		return v3Reply{}, false
	}
	// The agent's clock: kept for the next request, and for a retry after
	// a "not in time window" report.
	if len(f[0].body) > 0 {
		changed := !bytes.Equal(usr.engineID, f[0].body)
		usr.engineID, usr.boots, usr.etime, usr.timeAt = append([]byte(nil), f[0].body...), berInt64(f[1].body), berInt64(f[2].body), time.Now()
		if changed {
			usr.localize() // a megabyte of hashing: once per engine
		}
	}
	if flags&flagAuth != 0 {
		if usr.authKey == nil || len(f[4].body) != authLen(usr.authProto) {
			return v3Reply{}, false
		}
		got := append([]byte(nil), f[4].body...)
		zeroed := bytes.Replace(b, append([]byte{berOctets, byte(len(got))}, got...), append([]byte{berOctets, byte(len(got))}, make([]byte, len(got))...), 1)
		if !hmac.Equal(usr.mac(zeroed), got) {
			return v3Reply{}, false // forged or corrupted: ignored like a stray datagram
		}
	}
	scopedRaw := parts[3]
	if flags&flagPriv != 0 {
		if scopedRaw.tag != berOctets {
			return v3Reply{}, false
		}
		plain, err := usr.decrypt(scopedRaw.body, f[5].body, berInt64(f[1].body), berInt64(f[2].body))
		if err != nil {
			return v3Reply{}, false
		}
		if scopedRaw, _, err = berRead(plain); err != nil {
			return v3Reply{}, false
		}
	}
	sp, err := berChildren(scopedRaw.body, 3)
	if err != nil || len(sp) != 3 {
		return v3Reply{}, false
	}
	if sp[2].tag == snmpReport {
		vbs, err := readPDUAny(sp[2])
		if err != nil || len(vbs) == 0 {
			return v3Reply{report: "?"}, true
		}
		return v3Reply{report: vbs[0].OID}, true
	}
	if sp[2].tag != snmpResponse {
		return v3Reply{}, false
	}
	return v3Reply{pdu: sp[2]}, true
}

// readPDUAny reads a PDU's varbinds whatever its request ID (reports).
func readPDUAny(p berElem) ([]snmpVarbind, error) {
	f, err := berChildren(p.body, 4)
	if err != nil || len(f) != 4 {
		return nil, errors.New("malformed PDU")
	}
	return readPDU(p, berInt64(f[0].body))
}

// discover learns the agent's engine ID and clock with an empty request.
func (c *snmpClient) discover(ctx context.Context) error {
	msgID := int64(binary.BigEndian.Uint32(randBytes(4)) & 0x7FFFFFFF)
	header := tlv(berSeq, berInteger(msgID), berInteger(65507), tlv(berOctets, []byte{flagReportable}), berInteger(3))
	usm := tlv(berSeq, tlv(berOctets), berInteger(0), berInteger(0), tlv(berOctets), tlv(berOctets), tlv(berOctets))
	scoped := tlv(berSeq, tlv(berOctets), tlv(berOctets), tlv(snmpGet, berInteger(msgID), berInteger(0), berInteger(0), tlv(berSeq)))
	msg := tlv(berSeq, berInteger(3), header, tlv(berOctets, usm), scoped)
	_, err := c.exchange(ctx, msg, func(b []byte) bool {
		_, ok := c.readV3(b, msgID)
		return ok
	})
	if err != nil {
		return err
	}
	if len(c.v3.engineID) == 0 {
		return errors.New("SNMPv3 reply without an engine ID")
	}
	return nil
}

func authHash(proto string) func() hash.Hash {
	switch proto {
	case "md5":
		return md5.New
	case "sha", "sha1":
		return sha1.New
	case "sha256":
		return sha256.New
	case "sha512":
		return sha512.New
	}
	return nil
}

// authLen is the length of the digest carried in a message.
func authLen(proto string) int {
	switch proto {
	case "md5", "sha", "sha1":
		return 12
	case "sha256":
		return 24
	case "sha512":
		return 48
	}
	return 0
}

// passwordToKey turns a password into a key localized to an engine
// (RFC 3414 A.2): hash a megabyte of the repeated password, then hash the
// result with the engine ID on both sides.
func passwordToKey(h func() hash.Hash, pass string, engineID []byte) []byte {
	d := h()
	buf := make([]byte, 64)
	pw := []byte(pass)
	for i, n := 0, 0; n < 1<<20; n += 64 {
		for j := range buf {
			buf[j] = pw[i%len(pw)]
			i++
		}
		d.Write(buf)
	}
	ku := d.Sum(nil)
	d = h()
	d.Write(ku)
	d.Write(engineID)
	d.Write(ku)
	return d.Sum(nil)
}

func (u *usmUser) localize() {
	u.authKey, u.privKey = nil, nil
	h := authHash(u.authProto)
	if h == nil || u.authPass == "" {
		return
	}
	u.authKey = passwordToKey(h, u.authPass, u.engineID)
	if u.privPro != "" && u.privPass != "" {
		u.privKey = passwordToKey(h, u.privPass, u.engineID)
	}
}

func (u *usmUser) mac(msg []byte) []byte {
	m := hmac.New(authHash(u.authProto), u.authKey)
	m.Write(msg)
	return m.Sum(nil)[:authLen(u.authProto)]
}

func (u *usmUser) encrypt(plain []byte, boots, etime int64) (enc, salt []byte, err error) {
	if len(u.privKey) < 16 {
		return nil, nil, errors.New("SNMPv3 privacy needs authentication and a privacy password")
	}
	u.salt++
	switch u.privPro {
	case "des":
		salt = make([]byte, 8)
		binary.BigEndian.PutUint32(salt, uint32(boots))
		binary.BigEndian.PutUint32(salt[4:], uint32(u.salt))
		blk, err := des.NewCipher(u.privKey[:8])
		if err != nil {
			return nil, nil, err
		}
		iv := make([]byte, 8)
		for i := range iv {
			iv[i] = u.privKey[8+i] ^ salt[i]
		}
		if r := len(plain) % 8; r != 0 {
			plain = append(append([]byte(nil), plain...), make([]byte, 8-r)...)
		}
		enc = make([]byte, len(plain))
		cipher.NewCBCEncrypter(blk, iv).CryptBlocks(enc, plain)
		return enc, salt, nil
	case "aes", "aes128":
		salt = make([]byte, 8)
		binary.BigEndian.PutUint64(salt, u.salt^binary.BigEndian.Uint64(randBytes(8)))
		enc, err = aesCFB(u.privKey[:16], aesIV(boots, etime, salt), plain, true)
		return enc, salt, err
	}
	return nil, nil, fmt.Errorf("unknown privacy protocol %q", u.privPro)
}

func (u *usmUser) decrypt(enc, salt []byte, boots, etime int64) ([]byte, error) {
	if len(u.privKey) < 16 || len(salt) != 8 {
		return nil, errors.New("cannot decrypt")
	}
	switch u.privPro {
	case "des":
		if len(enc)%8 != 0 {
			return nil, errors.New("bad DES length")
		}
		blk, err := des.NewCipher(u.privKey[:8])
		if err != nil {
			return nil, err
		}
		iv := make([]byte, 8)
		for i := range iv {
			iv[i] = u.privKey[8+i] ^ salt[i]
		}
		out := make([]byte, len(enc))
		cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out, enc)
		return out, nil
	case "aes", "aes128":
		return aesCFB(u.privKey[:16], aesIV(boots, etime, salt), enc, false)
	}
	return nil, errors.New("unknown privacy protocol")
}

func aesIV(boots, etime int64, salt []byte) []byte {
	iv := make([]byte, 16)
	binary.BigEndian.PutUint32(iv, uint32(boots))
	binary.BigEndian.PutUint32(iv[4:], uint32(etime))
	copy(iv[8:], salt)
	return iv
}

// aesCFB is AES in 128-bit cipher feedback mode (RFC 3826).
func aesCFB(key, iv, in []byte, encrypt bool) ([]byte, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(in))
	prev := append([]byte(nil), iv...)
	ks := make([]byte, 16)
	for i := 0; i < len(in); i += 16 {
		blk.Encrypt(ks, prev)
		end := min(i+16, len(in))
		for j := i; j < end; j++ {
			out[j] = in[j] ^ ks[j-i]
		}
		if encrypt {
			copy(prev, out[i:end])
		} else {
			copy(prev, in[i:end])
		}
	}
	return out, nil
}
