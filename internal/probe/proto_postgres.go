package probe

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// postgresCheck sends a startup message and, with user= and password=,
// logs in (cleartext, md5 or SCRAM-SHA-256) and runs SELECT 1. Without
// credentials an authentication request or a login error already proves
// a PostgreSQL server is answering.
func postgresCheck(ctx context.Context, conn net.Conn, host string, o opts, r Result) Result {
	if o.on("ssl") {
		req := make([]byte, 8)
		binary.BigEndian.PutUint32(req, 8)
		binary.BigEndian.PutUint32(req[4:], 80877103)
		if _, err := conn.Write(req); err != nil {
			return protoFail(r, "SSL request", err)
		}
		var b [1]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return protoFail(r, "SSL request", err)
		}
		if b[0] != 'S' {
			return failKeep(r, ReasonAssertFailed, "the server does not accept SSL")
		}
		tc := tls.Client(conn, &tls.Config{ServerName: o.str("servername", host), InsecureSkipVerify: o.on("insecure")}) //nolint:gosec // opt-in
		if err := tc.HandshakeContext(ctx); err != nil {
			return failKeep(r, ReasonTLS, "TLS handshake failed: %v", rootErr(err))
		}
		conn = tc
	}
	user, pass := o.str("user", ""), o.str("password", "")
	login := user != ""
	if !login {
		user = "unagikuma"
	}
	db := o.str("database", user)
	var p []byte
	p = binary.BigEndian.AppendUint32(p, 196608) // protocol 3.0
	for _, kv := range [][2]string{{"user", user}, {"database", db}, {"application_name", "unagikuma"}} {
		p = append(p, kv[0]...)
		p = append(p, 0)
		p = append(p, kv[1]...)
		p = append(p, 0)
	}
	p = append(p, 0)
	if _, err := conn.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(p)+4)), p...)); err != nil {
		return protoFail(r, "startup", err)
	}
	br := bufio.NewReader(conn)
	send := func(typ byte, body []byte) error {
		m := append([]byte{typ}, binary.BigEndian.AppendUint32(nil, uint32(len(body)+4))...)
		_, err := conn.Write(append(m, body...))
		return err
	}
	var scram *scramClient
	version := ""
	for {
		typ, body, err := pgRead(br)
		if err != nil {
			return protoFail(r, "PostgreSQL", err)
		}
		switch typ {
		case 'E':
			code, msg := pgError(body)
			if !login {
				r.Reason, r.Detail = ReasonProtoReject, "PostgreSQL answers (no credentials given; login not tried)"
				return r
			}
			if strings.HasPrefix(code, "28") {
				return failKeep(r, ReasonAuthError, "PostgreSQL refused the login: %s", msg)
			}
			return failKeep(r, ReasonAssertFailed, "PostgreSQL: %s (%s)", msg, code)
		case 'R':
			if len(body) < 4 {
				return failKeep(r, ReasonProtoError, "short authentication message")
			}
			code := binary.BigEndian.Uint32(body)
			if code != 0 && !login {
				r.Reason, r.Detail = ReasonProtoReject, "PostgreSQL answers (no credentials given; login not tried)"
				return r
			}
			switch code {
			case 0: // AuthenticationOk
			case 3:
				err = send('p', append([]byte(pass), 0))
			case 5:
				if len(body) < 8 {
					return failKeep(r, ReasonProtoError, "short md5 salt")
				}
				inner := md5.Sum([]byte(pass + user))
				outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), body[4:8]...))
				err = send('p', append([]byte("md5"+hex.EncodeToString(outer[:])), 0))
			case 10:
				if !strings.Contains(string(body[4:]), "SCRAM-SHA-256\x00") {
					return failKeep(r, ReasonAuthError, "the server offers no supported SASL mechanism")
				}
				scram = newSCRAM(pass)
				first := scram.clientFirst()
				m := append([]byte("SCRAM-SHA-256"), 0)
				m = binary.BigEndian.AppendUint32(m, uint32(len(first)))
				err = send('p', append(m, first...))
			case 11:
				if scram == nil {
					return failKeep(r, ReasonProtoError, "unexpected SASL continue")
				}
				final, e := scram.clientFinal(string(body[4:]))
				if e != nil {
					return failKeep(r, ReasonProtoError, "SCRAM: %v", e)
				}
				err = send('p', []byte(final))
			case 12:
				if scram == nil || !scram.verify(string(body[4:])) {
					return failKeep(r, ReasonAuthError, "the server's SCRAM signature does not match")
				}
			default:
				return failKeep(r, ReasonAuthError, "unsupported authentication method %d", code)
			}
			if err != nil {
				return protoFail(r, "login", err)
			}
		case 'S':
			if k, v, ok := strings.Cut(strings.TrimRight(string(body), "\x00"), "\x00"); ok && k == "server_version" {
				version = v
			}
		case 'Z':
			// Ready for a query: prove the database answers one.
			if err := send('Q', append([]byte("SELECT 1"), 0)); err != nil {
				return protoFail(r, "query", err)
			}
			for {
				typ, body, err := pgRead(br)
				if err != nil {
					return protoFail(r, "query", err)
				}
				if typ == 'E' {
					_, msg := pgError(body)
					return failKeep(r, ReasonAssertFailed, "SELECT 1 failed: %s", msg)
				}
				if typ == 'Z' {
					_ = send('X', nil)
					r.Detail = "PostgreSQL login and SELECT 1 ok"
					if version != "" {
						r.Detail = "PostgreSQL " + version + ": login and SELECT 1 ok"
					}
					return r
				}
			}
		}
	}
}

func pgRead(br *bufio.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(h[1:])) - 4
	if n < 0 || n > 1<<20 {
		return 0, nil, errors.New("malformed message")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(br, b)
	return h[0], b, err
}

// pgError reads the SQLSTATE and message of an ErrorResponse.
func pgError(b []byte) (code, msg string) {
	for _, f := range strings.Split(string(b), "\x00") {
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case 'C':
			code = f[1:]
		case 'M':
			msg = f[1:]
		}
	}
	return code, msg
}

// scramClient is the client side of SCRAM-SHA-256 (RFC 5802/7677).
type scramClient struct {
	pass, nonce, firstBare, authMsg string
	serverKey                       []byte
}

func newSCRAM(pass string) *scramClient {
	return &scramClient{pass: pass, nonce: base64.RawStdEncoding.EncodeToString(randBytes(18))}
}

func (s *scramClient) clientFirst() string {
	s.firstBare = "n=,r=" + s.nonce
	return "n,," + s.firstBare
}

func (s *scramClient) clientFinal(serverFirst string) (string, error) {
	var nonce, salt string
	iter := 0
	for _, kv := range strings.Split(serverFirst, ",") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "r":
			nonce = v
		case "s":
			salt = v
		case "i":
			fmt.Sscan(v, &iter)
		}
	}
	if !strings.HasPrefix(nonce, s.nonce) || salt == "" || iter < 1 || iter > 1<<20 {
		return "", errors.New("bad server-first message")
	}
	saltB, err := base64.StdEncoding.DecodeString(salt)
	if err != nil {
		return "", err
	}
	salted, err := pbkdf2.Key(sha256.New, s.pass, saltB, iter, 32)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	clientKey := mac(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	withoutProof := "c=biws,r=" + nonce
	s.authMsg = s.firstBare + "," + serverFirst + "," + withoutProof
	sig := mac(stored[:], s.authMsg)
	proof := make([]byte, len(clientKey))
	for i := range clientKey {
		proof[i] = clientKey[i] ^ sig[i]
	}
	s.serverKey = mac(salted, "Server Key")
	return withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

func (s *scramClient) verify(serverFinal string) bool {
	v, ok := strings.CutPrefix(serverFinal, "v=")
	if !ok {
		return false
	}
	h := hmac.New(sha256.New, s.serverKey)
	h.Write([]byte(s.authMsg))
	want := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(v), []byte(want))
}
