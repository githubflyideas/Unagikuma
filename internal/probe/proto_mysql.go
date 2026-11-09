package probe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // mysql_native_password is defined with SHA-1
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
)

const (
	myLongPassword   = 0x00000001
	myConnectWithDB  = 0x00000008
	myProtocol41     = 0x00000200
	mySSL            = 0x00000800
	mySecureConn     = 0x00008000
	myPluginAuth     = 0x00080000
	myMaxPacket      = 1 << 24
	myCharsetUTF8MB4 = 45
)

// mysqlConn frames MySQL packets: 3-byte length, sequence number, payload.
type mysqlConn struct {
	c   net.Conn
	br  *bufio.Reader
	seq byte
}

func (m *mysqlConn) read() ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(m.br, h[:]); err != nil {
		return nil, err
	}
	n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
	m.seq = h[3] + 1
	if n > 1<<20 {
		return nil, errors.New("packet too large")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(m.br, b)
	return b, err
}

func (m *mysqlConn) write(p []byte) error {
	h := []byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), m.seq}
	m.seq++
	_, err := m.c.Write(append(h, p...))
	return err
}

func myError(p []byte) (int, string) {
	if len(p) < 3 {
		return 0, "error"
	}
	code := int(binary.LittleEndian.Uint16(p[1:]))
	msg := p[3:]
	if len(msg) > 6 && msg[0] == '#' {
		msg = msg[6:]
	}
	return code, string(msg)
}

func cstr(b []byte) (string, []byte) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return string(b), nil
	}
	return string(b[:i]), b[i+1:]
}

// scramble computes the auth response of the two common MySQL plugins.
func scramble(plugin, pass string, salt []byte) []byte {
	if pass == "" {
		return nil
	}
	switch plugin {
	case "caching_sha2_password":
		h1 := sha256.Sum256([]byte(pass))
		h2 := sha256.Sum256(h1[:])
		h3 := sha256.Sum256(append(h2[:], salt...))
		out := make([]byte, 32)
		for i := range out {
			out[i] = h1[i] ^ h3[i]
		}
		return out
	default: // mysql_native_password
		h1 := sha1.Sum([]byte(pass)) //nolint:gosec
		h2 := sha1.Sum(h1[:])        //nolint:gosec
		h3 := sha1.Sum(append(append([]byte{}, salt...), h2[:]...))
		out := make([]byte, 20)
		for i := range out {
			out[i] = h1[i] ^ h3[i]
		}
		return out
	}
}

// mysqlCheck reads the server greeting and, with user=, logs in and sends
// COM_PING. Without credentials the greeting proves MySQL answers.
func mysqlCheck(ctx context.Context, conn net.Conn, host string, o opts, r Result) Result {
	m := &mysqlConn{c: conn, br: bufio.NewReader(conn)}
	g, err := m.read()
	if err != nil {
		return protoFail(r, "MySQL greeting", err)
	}
	if len(g) > 0 && g[0] == 0xff {
		code, msg := myError(g)
		return failKeep(r, ReasonAuthError, "MySQL refuses this host: %s (%d)", msg, code)
	}
	if len(g) < 33 || g[0] != 10 {
		return failKeep(r, ReasonProtoError, "not a MySQL greeting")
	}
	version, rest := cstr(g[1:])
	if len(rest) < 4+8+1+2+1+2+2+1+10 {
		return failKeep(r, ReasonProtoError, "short MySQL greeting")
	}
	rest = rest[4:] // connection id
	salt := append([]byte{}, rest[:8]...)
	rest = rest[9:] // salt part 1 and filler
	caps := uint32(binary.LittleEndian.Uint16(rest))
	rest = rest[2+1+2:] // capabilities low, charset, status
	caps |= uint32(binary.LittleEndian.Uint16(rest)) << 16
	saltLen := int(rest[2])
	rest = rest[3+10:]
	n := max(13, saltLen-8)
	if len(rest) < n {
		n = len(rest)
	}
	salt = append(salt, bytes.TrimRight(rest[:n], "\x00")...)
	plugin := "mysql_native_password"
	if len(rest) > n {
		if p, _ := cstr(rest[n:]); p != "" {
			plugin = p
		}
	}
	user := o.str("user", "")
	if user == "" {
		r.Detail = "MySQL " + version + " answers (no credentials given; login not tried)"
		return r
	}
	want := uint32(myLongPassword | myProtocol41 | mySecureConn | myPluginAuth)
	db := o.str("database", "")
	if db != "" {
		want |= myConnectWithDB
	}
	head := func(c uint32) []byte {
		b := binary.LittleEndian.AppendUint32(nil, c)
		b = binary.LittleEndian.AppendUint32(b, myMaxPacket)
		b = append(b, myCharsetUTF8MB4)
		return append(b, make([]byte, 23)...)
	}
	secure := false
	if o.on("ssl") {
		if caps&mySSL == 0 {
			return failKeep(r, ReasonAssertFailed, "the server does not offer SSL")
		}
		want |= mySSL
		if err := m.write(head(want)); err != nil {
			return protoFail(r, "SSL request", err)
		}
		tc := tls.Client(conn, &tls.Config{ServerName: o.str("servername", host), InsecureSkipVerify: o.on("insecure")}) //nolint:gosec // opt-in
		if err := tc.HandshakeContext(ctx); err != nil {
			return failKeep(r, ReasonTLS, "TLS handshake failed: %v", rootErr(err))
		}
		m.c, m.br, secure = tc, bufio.NewReader(tc), true
	}
	pass := o.str("password", "")
	auth := scramble(plugin, pass, salt)
	resp := head(want)
	resp = append(append(resp, user...), 0)
	resp = append(append(resp, byte(len(auth))), auth...)
	if db != "" {
		resp = append(append(resp, db...), 0)
	}
	resp = append(append(resp, plugin...), 0)
	if err := m.write(resp); err != nil {
		return protoFail(r, "login", err)
	}
	for {
		p, err := m.read()
		if err != nil {
			return protoFail(r, "login", err)
		}
		if len(p) == 0 {
			return failKeep(r, ReasonProtoError, "empty reply")
		}
		switch p[0] {
		case 0x00: // OK: logged in
			m.seq = 0
			if err := m.write([]byte{0x0e}); err != nil { // COM_PING
				return protoFail(r, "ping", err)
			}
			q, err := m.read()
			if err != nil {
				return protoFail(r, "ping", err)
			}
			if len(q) == 0 || q[0] != 0x00 {
				code, msg := myError(q)
				return failKeep(r, ReasonAssertFailed, "COM_PING failed: %s (%d)", msg, code)
			}
			m.seq = 0
			_ = m.write([]byte{0x01}) // COM_QUIT
			r.Detail = "MySQL " + version + ": login and ping ok"
			return r
		case 0xff:
			code, msg := myError(p)
			if code == 1045 || code == 1044 || code == 1698 {
				return failKeep(r, ReasonAuthError, "MySQL refused the login: %s", msg)
			}
			return failKeep(r, ReasonAssertFailed, "MySQL: %s (%d)", msg, code)
		case 0xfe: // auth switch
			name, data := cstr(p[1:])
			plugin, salt = name, bytes.TrimRight(data, "\x00")
			if err := m.write(scramble(plugin, pass, salt)); err != nil {
				return protoFail(r, "login", err)
			}
		case 0x01: // more auth data (caching_sha2_password)
			if len(p) < 2 {
				return failKeep(r, ReasonProtoError, "short auth data")
			}
			switch p[1] {
			case 0x03: // fast auth ok; the OK packet follows
			case 0x04: // full auth: the password in clear over TLS, else RSA
				if secure {
					err = m.write(append([]byte(pass), 0))
				} else {
					err = m.write([]byte{0x02}) // ask for the public key
				}
				if err != nil {
					return protoFail(r, "login", err)
				}
			default:
				if p[1] == '-' { // PEM public key
					block, _ := pem.Decode(p[1:])
					if block == nil {
						return failKeep(r, ReasonProtoError, "bad server public key")
					}
					pub, err := x509.ParsePKIXPublicKey(block.Bytes)
					rk, ok := pub.(*rsa.PublicKey)
					if err != nil || !ok {
						return failKeep(r, ReasonProtoError, "bad server public key")
					}
					plain := append([]byte(pass), 0)
					for i := range plain {
						plain[i] ^= salt[i%len(salt)]
					}
					enc, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, rk, plain, nil) //nolint:gosec // MySQL's choice
					if err != nil {
						return failKeep(r, ReasonProtoError, "%v", err)
					}
					if err := m.write(enc); err != nil {
						return protoFail(r, "login", err)
					}
					continue
				}
				return failKeep(r, ReasonProtoError, "unexpected auth data")
			}
		default:
			return failKeep(r, ReasonProtoError, "unexpected reply 0x%02x", p[0])
		}
	}
}
