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
		tc, res, ok := pgStartTLS(ctx, conn, host, o, r)
		if !ok {
			return res
		}
		conn = tc
	}
	l := &pgLogin{conn: conn, pass: o.str("password", "")}
	user := o.str("user", "")
	l.login = user != ""
	if !l.login {
		user = "unagikuma"
	}
	l.user = user
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
	l.br = bufio.NewReader(conn)
	for {
		typ, body, err := pgRead(l.br)
		if err != nil {
			return protoFail(r, "PostgreSQL", err)
		}
		switch typ {
		case 'E':
			return l.refused(body, r)
		case 'R':
			if res, done := l.auth(body, r); done {
				return res
			}
		case 'S':
			if k, v, ok := strings.Cut(strings.TrimRight(string(body), "\x00"), "\x00"); ok && k == "server_version" {
				l.version = v
			}
		case 'Z':
			return l.selectOne(r)
		}
	}
}

// pgStartTLS asks the server for TLS and does the handshake.
func pgStartTLS(ctx context.Context, conn net.Conn, host string, o opts, r Result) (net.Conn, Result, bool) {
	req := make([]byte, 8)
	binary.BigEndian.PutUint32(req, 8)
	binary.BigEndian.PutUint32(req[4:], 80877103)
	if _, err := conn.Write(req); err != nil {
		return nil, protoFail(r, "SSL request", err), false
	}
	var b [1]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		return nil, protoFail(r, "SSL request", err), false
	}
	if b[0] != 'S' {
		return nil, failKeep(r, ReasonAssertFailed, "the server does not accept SSL"), false
	}
	tc := tls.Client(conn, &tls.Config{ServerName: o.str("servername", host), InsecureSkipVerify: o.on("insecure")}) //nolint:gosec // opt-in
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, failKeep(r, ReasonTLS, "TLS handshake failed: %v", rootErr(err)), false
	}
	return tc, r, true
}

// pgLogin follows the server's messages after the startup message.
type pgLogin struct {
	conn       net.Conn
	br         *bufio.Reader
	user, pass string
	login      bool // credentials were given
	scram      *scramClient
	version    string
}

func (l *pgLogin) send(typ byte, body []byte) error {
	m := append([]byte{typ}, binary.BigEndian.AppendUint32(nil, uint32(len(body)+4))...)
	_, err := l.conn.Write(append(m, body...))
	return err
}

// pgAnswers is the result without credentials: the server asked for them or
// refused, so it is PostgreSQL and it answers.
func pgAnswers(r Result) Result {
	r.Reason, r.Detail = ReasonProtoReject, "PostgreSQL answers (no credentials given; login not tried)"
	return r
}

func (l *pgLogin) refused(body []byte, r Result) Result {
	code, msg := pgError(body)
	if !l.login {
		return pgAnswers(r)
	}
	if strings.HasPrefix(code, "28") {
		return failKeep(r, ReasonAuthError, "PostgreSQL refused the login: %s", msg)
	}
	return failKeep(r, ReasonAssertFailed, "PostgreSQL: %s (%s)", msg, code)
}

// auth answers an authentication request; done says the check is over.
func (l *pgLogin) auth(body []byte, r Result) (Result, bool) {
	if len(body) < 4 {
		return failKeep(r, ReasonProtoError, "short authentication message"), true
	}
	code := binary.BigEndian.Uint32(body)
	if code != 0 && !l.login {
		return pgAnswers(r), true
	}
	var err error
	switch code {
	case 0: // AuthenticationOk
	case 3:
		err = l.send('p', append([]byte(l.pass), 0))
	case 5:
		if len(body) < 8 {
			return failKeep(r, ReasonProtoError, "short md5 salt"), true
		}
		inner := md5.Sum([]byte(l.pass + l.user))
		outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), body[4:8]...))
		err = l.send('p', append([]byte("md5"+hex.EncodeToString(outer[:])), 0))
	case 10, 11, 12:
		return l.sasl(code, body[4:], r)
	default:
		return failKeep(r, ReasonAuthError, "unsupported authentication method %d", code), true
	}
	if err != nil {
		return protoFail(r, "login", err), true
	}
	return r, false
}

// sasl runs the SCRAM-SHA-256 exchange.
func (l *pgLogin) sasl(code uint32, data []byte, r Result) (Result, bool) {
	var err error
	switch code {
	case 10:
		if !strings.Contains(string(data), "SCRAM-SHA-256\x00") {
			return failKeep(r, ReasonAuthError, "the server offers no supported SASL mechanism"), true
		}
		l.scram = newSCRAM(l.pass)
		first := l.scram.clientFirst()
		m := append([]byte("SCRAM-SHA-256"), 0)
		m = binary.BigEndian.AppendUint32(m, uint32(len(first)))
		err = l.send('p', append(m, first...))
	case 11:
		if l.scram == nil {
			return failKeep(r, ReasonProtoError, "unexpected SASL continue"), true
		}
		final, e := l.scram.clientFinal(string(data))
		if e != nil {
			return failKeep(r, ReasonProtoError, "SCRAM: %v", e), true
		}
		err = l.send('p', []byte(final))
	case 12:
		if l.scram == nil || !l.scram.verify(string(data)) {
			return failKeep(r, ReasonAuthError, "the server's SCRAM signature does not match"), true
		}
	}
	if err != nil {
		return protoFail(r, "login", err), true
	}
	return r, false
}

// selectOne proves the database answers a query once it is ready for one.
func (l *pgLogin) selectOne(r Result) Result {
	if err := l.send('Q', append([]byte("SELECT 1"), 0)); err != nil {
		return protoFail(r, "query", err)
	}
	for {
		typ, body, err := pgRead(l.br)
		if err != nil {
			return protoFail(r, "query", err)
		}
		if typ == 'E' {
			_, msg := pgError(body)
			return failKeep(r, ReasonAssertFailed, "SELECT 1 failed: %s", msg)
		}
		if typ == 'Z' {
			_ = l.send('X', nil)
			r.Detail = "PostgreSQL login and SELECT 1 ok"
			if l.version != "" {
				r.Detail = "PostgreSQL " + l.version + ": login and SELECT 1 ok"
			}
			return r
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
