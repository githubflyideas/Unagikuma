package probe

import (
	"bufio"
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// serve accepts connections on 127.0.0.1 and hands each to h.
func serve(t *testing.T, h func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); h(c) }()
		}
	}()
	return ln.Addr().String()
}

// ---- Redis ----

func fakeRedis(pass, role string) func(net.Conn) {
	return func(c net.Conn) {
		br := bufio.NewReader(c)
		authed := pass == ""
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			n := 0
			fmt.Sscanf(line, "*%d", &n)
			var args []string
			for i := 0; i < n; i++ {
				br.ReadString('\n')
				a, _ := br.ReadString('\n')
				args = append(args, strings.TrimRight(a, "\r\n"))
			}
			switch strings.ToUpper(args[0]) {
			case "AUTH":
				if args[len(args)-1] == pass {
					authed = true
					c.Write([]byte("+OK\r\n"))
				} else {
					c.Write([]byte("-WRONGPASS invalid username-password pair\r\n"))
				}
			case "PING":
				if !authed {
					c.Write([]byte("-NOAUTH Authentication required.\r\n"))
				} else {
					c.Write([]byte("+PONG\r\n"))
				}
			case "ROLE":
				c.Write([]byte(fmt.Sprintf("*3\r\n$%d\r\n%s\r\n:0\r\n*0\r\n", len(role), role)))
			}
		}
	}
}

func TestRedis(t *testing.T) {
	open := serve(t, fakeRedis("", "master"))
	r := run(t, Spec{Type: "TCP", Target: open, Opts: map[string]string{"proto": "redis"}})
	want(t, r, VoteOK, ReasonOK)
	if DepthOf(Spec{Type: "TCP", Target: open, Opts: map[string]string{"proto": "redis"}}) != "L3" {
		t.Fatal("depth")
	}
	want(t, run(t, Spec{Type: "TCP", Target: open, Opts: map[string]string{"proto": "redis", "role": "master"}}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "TCP", Target: open, Opts: map[string]string{"proto": "redis", "role": "replica"}}), VoteFail, ReasonAssertFailed)

	locked := serve(t, fakeRedis("s3cret", "slave"))
	want(t, run(t, Spec{Type: "TCP", Target: locked, Opts: map[string]string{"proto": "redis"}}), VoteFail, ReasonAuthError)
	want(t, run(t, Spec{Type: "TCP", Target: locked, Opts: map[string]string{"proto": "redis", "password": "nope"}}), VoteFail, ReasonAuthError)
	want(t, run(t, Spec{Type: "TCP", Target: locked, Opts: map[string]string{"proto": "redis", "password": "s3cret", "role": "replica"}}), VoteOK, ReasonOK)
}

// ---- PostgreSQL ----

func pgSend(c net.Conn, typ byte, body []byte) {
	m := append([]byte{typ}, binary.BigEndian.AppendUint32(nil, uint32(len(body)+4))...)
	c.Write(append(m, body...))
}

func pgAuth(code uint32, extra []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, code), extra...)
}

// fakePostgres answers a startup with method ("md5" or "scram") and checks
// the password.
func fakePostgres(method, user, pass string) func(net.Conn) {
	return func(c net.Conn) {
		br := bufio.NewReader(c)
		var h [4]byte
		io.ReadFull(br, h[:])
		body := make([]byte, binary.BigEndian.Uint32(h[:])-4)
		io.ReadFull(br, body)
		fields := strings.Split(string(body[4:]), "\x00")
		got := map[string]string{}
		for i := 0; i+1 < len(fields); i += 2 {
			got[fields[i]] = fields[i+1]
		}
		if got["user"] != user {
			pgSend(c, 'E', []byte("SFATAL\x00C28000\x00Mrole \""+got["user"]+"\" does not exist\x00\x00"))
			return
		}
		fail := func() { pgSend(c, 'E', []byte("SFATAL\x00C28P01\x00Mpassword authentication failed\x00\x00")) }
		switch method {
		case "md5":
			salt := []byte{1, 2, 3, 4}
			pgSend(c, 'R', pgAuth(5, salt))
			typ, msg, _ := pgRead(br)
			inner := md5.Sum([]byte(pass + user))
			outer := md5.Sum(append([]byte(hex.EncodeToString(inner[:])), salt...))
			if typ != 'p' || strings.TrimRight(string(msg), "\x00") != "md5"+hex.EncodeToString(outer[:]) {
				fail()
				return
			}
		case "scram":
			pgSend(c, 'R', pgAuth(10, []byte("SCRAM-SHA-256\x00\x00")))
			_, msg, _ := pgRead(br)
			_, rest, _ := strings.Cut(string(msg), "\x00")
			clientFirst := rest[4:]
			bare := strings.TrimPrefix(clientFirst, "n,,")
			cnonce := bare[strings.Index(bare, "r=")+2:]
			salt := []byte("saltsaltsalt")
			serverFirst := "r=" + cnonce + "srv,s=" + base64.StdEncoding.EncodeToString(salt) + ",i=4096"
			pgSend(c, 'R', pgAuth(11, []byte(serverFirst)))
			_, final, _ := pgRead(br)
			fs := string(final)
			idx := strings.Index(fs, ",p=")
			salted, _ := pbkdf2.Key(sha256.New, pass, salt, 4096, 32)
			mac := func(k []byte, m string) []byte { h := hmac.New(sha256.New, k); h.Write([]byte(m)); return h.Sum(nil) }
			ck := mac(salted, "Client Key")
			stored := sha256.Sum256(ck)
			auth := bare + "," + serverFirst + "," + fs[:idx]
			sig := mac(stored[:], auth)
			proof, _ := base64.StdEncoding.DecodeString(fs[idx+3:])
			for i := range proof {
				proof[i] ^= sig[i]
			}
			if h := sha256.Sum256(proof); !hmac.Equal(h[:], stored[:]) {
				fail()
				return
			}
			pgSend(c, 'R', pgAuth(12, []byte("v="+base64.StdEncoding.EncodeToString(mac(mac(salted, "Server Key"), auth)))))
		}
		pgSend(c, 'R', pgAuth(0, nil))
		pgSend(c, 'S', []byte("server_version\x0016.4\x00"))
		pgSend(c, 'Z', []byte("I"))
		if typ, q, _ := pgRead(br); typ == 'Q' && strings.HasPrefix(string(q), "SELECT 1") {
			pgSend(c, 'T', []byte{0, 0})
			pgSend(c, 'C', []byte("SELECT 1\x00"))
			pgSend(c, 'Z', []byte("I"))
		}
		pgRead(br)
	}
}

func TestPostgres(t *testing.T) {
	for _, method := range []string{"md5", "scram"} {
		addr := serve(t, fakePostgres(method, "mon", "pw"))
		r := run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "postgres", "user": "mon", "password": "pw"}})
		want(t, r, VoteOK, ReasonOK)
		if !strings.Contains(r.Detail, "16.4") {
			t.Fatalf("%s: %s", method, r.Detail)
		}
		want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "postgres", "user": "mon", "password": "bad"}}), VoteFail, ReasonAuthError)
		// No credentials: the server answering is enough.
		want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "postgres"}}), VoteOK, ReasonProtoReject)
	}
	notPG := serve(t, func(c net.Conn) { c.Write([]byte("SSH-2.0-OpenSSH_9\r\n")) })
	if r := run(t, Spec{Type: "TCP", Target: notPG, Opts: map[string]string{"proto": "postgres", "user": "x"}}); r.Vote != VoteFail {
		t.Fatalf("not postgres: %+v", r)
	}
}

// ---- MySQL ----

func myPacket(c net.Conn, seq byte, p []byte) {
	c.Write(append([]byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), seq}, p...))
}

func myRead(br *bufio.Reader) (byte, []byte) {
	var h [4]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return 0, nil
	}
	b := make([]byte, int(h[0])|int(h[1])<<8|int(h[2])<<16)
	io.ReadFull(br, b)
	return h[3], b
}

// fakeMySQL greets with plugin and checks the scramble; full makes
// caching_sha2_password ask for the RSA path.
func fakeMySQL(plugin, user, pass string, full bool) func(net.Conn) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	return func(c net.Conn) {
		salt := []byte("abcdefghijklmnopqrst")
		g := []byte{10}
		g = append(g, "8.4.2\x00"...)
		g = append(g, 1, 0, 0, 0)
		g = append(g, salt[:8]...)
		g = append(g, 0)
		caps := uint32(myProtocol41 | mySecureConn | myPluginAuth | myLongPassword | myConnectWithDB)
		g = binary.LittleEndian.AppendUint16(g, uint16(caps))
		g = append(g, 45, 2, 0)
		g = binary.LittleEndian.AppendUint16(g, uint16(caps>>16))
		g = append(g, 21)
		g = append(g, make([]byte, 10)...)
		g = append(g, salt[8:]...)
		g = append(g, 0)
		g = append(g, plugin...)
		g = append(g, 0)
		myPacket(c, 0, g)
		br := bufio.NewReader(c)
		_, resp := myRead(br)
		if len(resp) < 34 {
			return // the client only wanted the greeting
		}
		rest := resp[32:]
		u, rest := cstr(rest)
		n := int(rest[0])
		auth := rest[1 : 1+n]
		ok := u == user && string(auth) == string(scramble(plugin, pass, salt))
		seq := byte(2)
		if ok && plugin == "caching_sha2_password" {
			if !full {
				myPacket(c, seq, []byte{0x01, 0x03})
				seq++
			} else {
				myPacket(c, seq, []byte{0x01, 0x04})
				_, req := myRead(br)
				if len(req) != 1 || req[0] != 0x02 {
					return
				}
				der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
				myPacket(c, seq+2, append([]byte{0x01}, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})...))
				_, enc := myRead(br)
				plain, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, key, enc, nil) //nolint:gosec
				for i := range plain {
					plain[i] ^= salt[i%len(salt)]
				}
				ok = err == nil && string(plain) == pass+"\x00"
				seq += 4
			}
		}
		if !ok {
			myPacket(c, seq, append([]byte{0xff, 0x15, 0x04}, "#28000Access denied for user"...))
			return
		}
		myPacket(c, seq, []byte{0, 0, 0, 2, 0, 0, 0})
		if _, p := myRead(br); len(p) == 1 && p[0] == 0x0e {
			myPacket(c, 1, []byte{0, 0, 0, 2, 0, 0, 0})
		}
	}
}

func TestMySQL(t *testing.T) {
	cases := []struct {
		plugin string
		full   bool
	}{{"mysql_native_password", false}, {"caching_sha2_password", false}, {"caching_sha2_password", true}}
	for _, tc := range cases {
		addr := serve(t, fakeMySQL(tc.plugin, "mon", "pw", tc.full))
		r := run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "mysql", "user": "mon", "password": "pw"}})
		if r.Vote != VoteOK || !strings.Contains(r.Detail, "8.4.2") {
			t.Fatalf("%s full=%v: %+v", tc.plugin, tc.full, r)
		}
		want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "mysql", "user": "mon", "password": "bad"}}), VoteFail, ReasonAuthError)
		want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "mysql"}}), VoteOK, ReasonOK)
	}
	refused := serve(t, func(c net.Conn) {
		myPacket(c, 0, append([]byte{0xff, 0x6a, 0x04}, "Host '10.0.0.9' is not allowed to connect"...))
	})
	want(t, run(t, Spec{Type: "TCP", Target: refused, Opts: map[string]string{"proto": "mysql"}}), VoteFail, ReasonAuthError)
}

// ---- gRPC ----

func TestGRPCHealth(t *testing.T) {
	status := map[string]byte{"": 1, "api": 1, "down": 2}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		svc := ""
		if len(body) > 7 {
			svc = string(body[7:])
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		st, ok := status[svc]
		if !ok {
			w.Header().Set("Grpc-Status", "5")
			return
		}
		w.Write([]byte{0, 0, 0, 0, 2, 0x08, st})
		w.Header().Set("Grpc-Status", "0")
	})
	var protos http.Protocols
	protos.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: h, Protocols: &protos}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	addr := ln.Addr().String()
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "grpc"}}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "grpc", "service": "api"}}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "grpc", "service": "down"}}), VoteFail, ReasonAssertFailed)
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"proto": "grpc", "service": "nope"}}), VoteFail, ReasonAssertFailed)
	plain := serve(t, func(c net.Conn) { c.Write([]byte("HTTP/1.1 200 OK\r\n\r\n")) })
	if r := run(t, Spec{Type: "TCP", Target: plain, Opts: map[string]string{"proto": "grpc"}}); r.Vote != VoteFail {
		t.Fatalf("plain HTTP/1: %+v", r)
	}
}

// ---- Docker ----

func TestDocker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Docker checks read a unix socket")
	}
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets: %v", err)
	}
	containers := map[string]string{
		"web":   `{"State":{"Status":"running","Running":true},"Config":{"Image":"nginx:1"}}`,
		"api":   `{"State":{"Status":"running","Running":true,"Health":{"Status":"unhealthy","Log":[{"Output":"curl: (7) refused"}]}}}`,
		"db":    `{"State":{"Status":"exited","Running":false,"ExitCode":137}}`,
		"cache": `{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}`,
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
		body, ok := containers[name]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	o := map[string]string{"socket": sock}
	want(t, run(t, Spec{Type: "DOCKER", Target: "web", Opts: o}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "DOCKER", Target: "cache", Opts: o}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "DOCKER", Target: "api", Opts: o}), VoteFail, ReasonAssertFailed)
	want(t, run(t, Spec{Type: "DOCKER", Target: "db", Opts: o}), VoteFail, ReasonAssertFailed)
	want(t, run(t, Spec{Type: "DOCKER", Target: "ghost", Opts: o}), VoteFail, ReasonAssertFailed)
	want(t, run(t, Spec{Type: "DOCKER", Target: "web", Opts: map[string]string{"socket": filepath.Join(dir, "none.sock")}}), VoteAbstain, ReasonPlatform)
}

func TestProtoValidation(t *testing.T) {
	bad := []map[string]string{
		{"proto": "oracle"},
		{"user": "x"},
		{"proto": "postgres", "tls": "yes"},
		{"proto": "redis", "ssl": "yes"},
		{"proto": "redis", "send": "PING"},
		{"proto": "mysql", "role": "master"},
		{"proto": "redis", "role": "boss"},
	}
	for _, o := range bad {
		if err := Validate(Spec{Type: "TCP", Target: "10.0.0.1:1", Opts: o}); err == nil {
			t.Errorf("accepted %v", o)
		}
	}
	if err := Validate(Spec{Type: "DOCKER", Target: "a/b"}); err == nil {
		t.Error("docker target with a slash accepted")
	}
}
