package probe

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/keywords"
)

func run(t *testing.T, s Spec) Result {
	t.Helper()
	if s.Timeout == 0 {
		s.Timeout = 2 * time.Second
	}
	return Run(context.Background(), s)
}

func want(t *testing.T, r Result, vote Vote, reason string) {
	t.Helper()
	if r.Vote != vote || r.Reason != reason {
		t.Fatalf("got %s/%s (%s), want %s/%s", r.Vote, r.Reason, r.Detail, vote, reason)
	}
}

// ---- HTTP ----

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, `{"status":"ok","items":[{"n":7}],"ver":"1.2"} 登录`)
		case "/500":
			w.WriteHeader(500)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/headers":
			if r.Header.Get("X-Api-Key") != "k1" || r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
			}
		}
	}))
	defer srv.Close()
	cases := []struct {
		path   string
		opts   map[string]string
		vote   Vote
		reason string
	}{
		{"/ok", nil, VoteOK, ReasonOK},
		{"/ok", map[string]string{"keyword": "登录"}, VoteOK, ReasonOK},
		{"/ok", map[string]string{"keyword": "nope"}, VoteFail, ReasonAssertFailed},
		{"/ok", map[string]string{"absent": "登录"}, VoteFail, ReasonAssertFailed},
		{"/ok", map[string]string{"keyword-regex": `ver":"1\.\d`}, VoteOK, ReasonOK},
		{"/ok", map[string]string{"json-path": "$.status", "json-expect": "ok"}, VoteOK, ReasonOK},
		{"/ok", map[string]string{"json-path": "$.items[0].n", "json-expect-num": ">5"}, VoteOK, ReasonOK},
		{"/ok", map[string]string{"json-path": "$.items[0].n", "json-expect-num": ">=8"}, VoteFail, ReasonAssertFailed},
		{"/ok", map[string]string{"json-path": "$.missing"}, VoteFail, ReasonAssertFailed},
		{"/500", nil, VoteFail, ReasonHTTPStatus},
		{"/500", map[string]string{"expect": "500"}, VoteOK, ReasonOK},
		{"/redirect", nil, VoteOK, ReasonOK},
		{"/redirect", map[string]string{"no-follow": "yes"}, VoteOK, ReasonOK},
		{"/redirect", map[string]string{"no-follow": "yes", "expect": "200"}, VoteFail, ReasonHTTPStatus},
		{"/headers", map[string]string{"header.X-Api-Key": "k1", "bearer": "tok"}, VoteOK, ReasonOK},
		{"/headers", nil, VoteFail, ReasonHTTPStatus},
	}
	for _, c := range cases {
		r := run(t, Spec{Type: "HTTP", Target: srv.URL + c.path, Opts: c.opts})
		if r.Vote != c.vote || r.Reason != c.reason {
			t.Errorf("%s %v: %s/%s (%s)", c.path, c.opts, r.Vote, r.Reason, r.Detail)
		}
		if r.Depth != keywords.L3 {
			t.Errorf("depth %s", r.Depth)
		}
	}
	r := run(t, Spec{Type: "HTTP", Target: srv.URL + "/headers", Opts: map[string]string{"header.X-Api-Key": "k1", "bearer": "tok"}})
	if ev := string(r.Evidence.Request); strings.Contains(ev, "tok") || strings.Contains(ev, "k1") {
		t.Fatalf("credentials leaked into evidence: %s", ev)
	}
	if r.Evidence.Peer == "" || r.Latency < 0 || !coarseClock && r.Latency == 0 {
		t.Fatalf("peer/latency missing: %+v", r)
	}
}

// shortCertServer is a TLS server whose self-signed certificate expires
// in 10 days.
func shortCertServer(t *testing.T) *httptest.Server {
	return certServer(t, time.Now().Add(10*24*time.Hour))
}

// certServer is a TLS server with a self-signed certificate valid until
// notAfter (made at run time: no key material in the repository).
func certServer(t *testing.T, notAfter time.Time) *httptest.Server {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: notAfter.Add(-30 * 24 * time.Hour), NotAfter: notAfter,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPTLSAndCert(t *testing.T) {
	srv := shortCertServer(t)
	// Self-signed: rejected unless insecure.
	want(t, run(t, Spec{Type: "HTTP", Target: srv.URL}), VoteFail, ReasonTLS)
	r := run(t, Spec{Type: "HTTP", Target: srv.URL, Opts: map[string]string{"insecure": "yes"}})
	want(t, r, VoteOK, ReasonOK)
	days := r.Metrics["cert.days"]
	if days <= 0 {
		t.Fatalf("cert.days %v", days)
	}
	r = run(t, Spec{Type: "HTTP", Target: srv.URL, Opts: map[string]string{"insecure": "yes", "cert-days": fmt.Sprint(int(days) + 10)}})
	if r.Vote != VoteOK || !r.Degraded {
		t.Fatalf("cert-days: %+v", r)
	}
	want(t, run(t, Spec{Type: "HTTP", Target: srv.URL, Opts: map[string]string{"insecure": "yes", "cert-critical-days": fmt.Sprint(int(days) + 10)}}), VoteFail, ReasonTLS)
}

// Skipping verification (insecure=yes) must not hide an expired
// certificate: it fails whatever the thresholds.
func TestExpiredCertFailsEvenInsecure(t *testing.T) {
	srv := certServer(t, time.Now().Add(-48*time.Hour))
	r := run(t, Spec{Type: "HTTP", Target: srv.URL, Opts: map[string]string{"insecure": "yes"}})
	want(t, r, VoteFail, ReasonTLS)
	if !strings.Contains(r.Detail, "expired") {
		t.Fatalf("detail %q", r.Detail)
	}
}

func TestHTTPErrors(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	want(t, run(t, Spec{Type: "HTTP", Target: "http://" + addr + "/"}), VoteFail, ReasonConnRefused)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer slow.Close()
	want(t, run(t, Spec{Type: "HTTP", Target: slow.URL, Timeout: 200 * time.Millisecond}), VoteFail, ReasonTimeout)

	// An authoritative "no such name", without asking a real resolver.
	defer func(f func(context.Context, string, string) ([]netip.Addr, error)) { lookupNetIP = f }(lookupNetIP)
	lookupNetIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	want(t, run(t, Spec{Type: "HTTP", Target: "http://name.invalid/"}), VoteFail, ReasonDNSNXDomain)
	// The resolver never answers: out of time before there was an address
	// is the resolver failing, not the target timing out.
	// (The transport dials in a goroutine of its own that outlives the
	// request: let it go and wait for it before lookupNetIP is put back.)
	release, looked := make(chan struct{}), make(chan struct{})
	lookupNetIP = func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		defer close(looked)
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil, context.DeadlineExceeded
	}
	want(t, run(t, Spec{Type: "HTTP", Target: "http://name.invalid/", Timeout: 200 * time.Millisecond}), VoteFail, ReasonDNSResolve)
	close(release)
	<-looked
	want(t, run(t, Spec{Type: "HTTP", Target: "http://169.254.169.254/"}), VoteAbstain, ReasonConfig)
	want(t, run(t, Spec{Type: "HTTP", Target: "ftp://x/"}), VoteAbstain, ReasonConfig)
}

func TestValidate(t *testing.T) {
	bad := []Spec{
		{Type: "HTTP", Target: "https://x", Opts: map[string]string{"nope": "1"}},
		{Type: "HTTP", Target: "https://x", Opts: map[string]string{"expect": "abc"}},
		{Type: "HTTP", Target: "https://x", Opts: map[string]string{"json-expect": "x"}},
		{Type: "HTTP", Target: "https://x", Opts: map[string]string{"keyword-regex": "("}},
		{Type: "TCP", Target: "x", Opts: nil},
		{Type: "TCP", Target: "x:1", Opts: map[string]string{"send-hex": "zz"}},
		{Type: "PING", Target: "x", Opts: map[string]string{"loss": "200%"}},
		{Type: "UDP", Target: "x:dns", Opts: map[string]string{"qtype": "ANY"}},
		{Type: "UDP", Target: "x:dnsix"},
		{Type: "UDP", Target: "x:snmp", Opts: map[string]string{"version": "4"}},
		{Type: "UDP", Target: "x:ntp", Opts: map[string]string{"query": "x"}},
		{Type: "UDP", Target: "x:9999", Opts: map[string]string{"accept-from": "anyone"}},
		{Type: "FOO", Target: "x"},
	}
	for _, s := range bad {
		if Validate(s) == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	good := []Spec{
		{Type: "HTTP", Target: "https://x", Opts: map[string]string{"expect": "200,301-399", "cert-days": "14", "header.X-A": "b", "family": "4"}},
		{Type: "UDP", Target: "x:isakmp", Opts: map[string]string{"ike": "v2"}}, // template not built: options accepted
		{Type: "UDP", Target: "x:dns", Opts: map[string]string{"query": "example.com", "qtype": "SOA", "authorized": "yes"}},
		{Type: "UDP", Target: "x:9999", Opts: map[string]string{"send-hex": "01 {{nonce4}}", "expect": "any"}},
	}
	for _, s := range good {
		if err := Validate(s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

func TestDepthOf(t *testing.T) {
	cases := map[string]keywords.Depth{
		"x:dns": keywords.L3, "x:ntp": keywords.L3, "x:syslog": keywords.L1, "x:isakmp": keywords.L1,
		"x:9999": keywords.L1, "x:nameserver": keywords.L0,
	}
	for target, d := range cases {
		if got := DepthOf(Spec{Type: "UDP", Target: target}); got != d {
			t.Errorf("%s: %s want %s", target, got, d)
		}
	}
	if DepthOf(Spec{Type: "UDP", Target: "x:9999", Opts: map[string]string{"expect": "any"}}) != keywords.L2 {
		t.Error("raw with expect")
	}
	if DepthOf(Spec{Type: "TCP", Target: "x:1"}) != keywords.L2 || DepthOf(Spec{Type: "TCP", Target: "x:1", Opts: map[string]string{"tls": "yes"}}) != keywords.L3 {
		t.Error("tcp depth")
	}
}

// ---- TCP ----

func TestTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				fmt.Fprint(c, "220 smtp ready\r\n")
				line, _ := bufio.NewReader(c).ReadString('\n')
				if strings.HasPrefix(line, "PING") {
					fmt.Fprint(c, "PONG\r\n")
				}
			}(c)
		}
	}()
	addr := l.Addr().String()
	want(t, run(t, Spec{Type: "TCP", Target: addr}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"expect": "220"}}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"send": `PING\r\n`, "expect": "PONG"}}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"expect": "SSH-2.0"}}), VoteFail, ReasonAssertFailed)
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l2.Addr().String()
	l2.Close()
	want(t, run(t, Spec{Type: "TCP", Target: closed}), VoteFail, ReasonConnRefused)
}

func TestTCPTLS(t *testing.T) {
	srv := shortCertServer(t)
	addr := strings.TrimPrefix(srv.URL, "https://")
	want(t, run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"tls": "yes"}}), VoteFail, ReasonTLS)
	r := run(t, Spec{Type: "TCP", Target: addr, Opts: map[string]string{"tls": "yes", "insecure": "yes"}})
	want(t, r, VoteOK, ReasonOK)
	if r.Metrics["cert.days"] <= 0 {
		t.Fatalf("cert days: %v", r.Metrics)
	}
	_ = tls.VersionTLS13
}

// coarseClock: Windows' monotonic clock advances in timer ticks, so a
// loopback round trip can measure 0 there.
var coarseClock = runtime.GOOS == "windows"

// ---- PING ----

func TestPingLoopback(t *testing.T) {
	r := run(t, Spec{Type: "PING", Target: "127.0.0.1", Opts: map[string]string{"count": "2", "interval": "50ms", "fallback": "off"}})
	switch {
	case r.Vote == VoteAbstain && (r.Reason == ReasonPrivilege || r.Reason == ReasonPlatform):
		t.Skipf("ICMP not available in this sandbox: %s", r.Detail)
	case r.Vote != VoteOK:
		t.Fatalf("%+v", r)
	}
	if r.Metrics["ping.loss"] != 0 || r.Metrics["ping.rtt_avg_ms"] < 0 || !coarseClock && r.Metrics["ping.rtt_avg_ms"] == 0 {
		t.Fatalf("metrics %v", r.Metrics)
	}
}

// The smallest ping (size=8) leaves 4 random bytes after the ID and
// sequence; matching an ICMP error must not read past them.
func TestPingSmallSize(t *testing.T) {
	if got := nonceTag(make([]byte, 8)); len(got) != 4 {
		t.Fatalf("tag of an 8-byte request: %d bytes", len(got))
	}
	if got := nonceTag(make([]byte, 32)); len(got) != 8 {
		t.Fatalf("tag of a 32-byte request: %d bytes", len(got))
	}
	r := run(t, Spec{Type: "PING", Target: "127.0.0.1", Opts: map[string]string{"count": "1", "size": "8", "fallback": "off"}})
	switch {
	case r.Vote == VoteAbstain && (r.Reason == ReasonPrivilege || r.Reason == ReasonPlatform):
		t.Skipf("ICMP not available in this sandbox: %s", r.Detail)
	case r.Vote != VoteOK:
		t.Fatalf("%+v", r)
	}
}

// Without ping permission a host that accepts or refuses a TCP connection
// is up; the result says it was checked over TCP.
func TestPingTCPFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	open := uint16(ln.Addr().(*net.TCPAddr).Port)
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	refused := uint16(closed.Addr().(*net.TCPAddr).Port)
	closed.Close()
	saved := tcpReachPorts
	defer func() { tcpReachPorts = saved }()
	ip := netip.MustParseAddr("127.0.0.1")
	for _, ports := range [][]uint16{{open}, {refused}} {
		tcpReachPorts = ports
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r := tcpReach(ctx, ip)
		cancel()
		if r.Vote != VoteOK || !strings.Contains(r.Detail, "checked over TCP") {
			t.Fatalf("ports %v: %+v", ports, r)
		}
	}
	if err := validatePing(opts{"fallback": "bogus"}); err == nil {
		t.Fatal("fallback=bogus accepted")
	}
}

// ---- UDP ----

// udpServer answers datagrams with handler(req) (nil = no reply).
func udpServer(t *testing.T, handler func(req []byte) []byte) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := handler(append([]byte(nil), buf[:n]...)); resp != nil {
				c.WriteTo(resp, from)
			}
		}
	}()
	return c.LocalAddr().String()
}

func closedUDPPort(t *testing.T) string {
	c, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := c.LocalAddr().String()
	c.Close()
	return addr
}

func udpSpec(addr, template string, o map[string]string) Spec {
	if o == nil {
		o = map[string]string{}
	}
	o["template"] = template
	return Spec{Type: "UDP", Target: addr, Opts: o, Timeout: time.Second}
}

func TestUDPClosedPort(t *testing.T) {
	addr := closedUDPPort(t)
	for _, tmpl := range []string{"l1", "dns", "ntp", "echo"} {
		r := run(t, udpSpec(addr, tmpl, nil))
		want(t, r, VoteFail, ReasonPortUnreachable)
	}
}

func TestUDPEcho(t *testing.T) {
	addr := udpServer(t, func(req []byte) []byte { return req })
	want(t, run(t, udpSpec(addr, "echo", nil)), VoteOK, ReasonOK)
	bad := udpServer(t, func(req []byte) []byte { return []byte("nope") })
	want(t, run(t, udpSpec(bad, "echo", nil)), VoteFail, ReasonAssertFailed)
	silent := udpServer(t, func(req []byte) []byte { return nil })
	r := run(t, udpSpec(silent, "echo", map[string]string{"retransmit": "0"}))
	want(t, r, VoteFail, ReasonTimeout)
}

func TestUDPL1(t *testing.T) {
	silent := udpServer(t, func(req []byte) []byte { return nil })
	r := run(t, Spec{Type: "UDP", Target: strings.Replace(silent, ":", ":", 1), Opts: map[string]string{"template": "syslog"}, Timeout: 300 * time.Millisecond})
	want(t, r, VoteOK, ReasonOK)
	if r.Depth != keywords.L1 {
		t.Fatalf("depth %s", r.Depth)
	}
}

// A silent port is "probably open" only while its host answers: silence
// from a host that is gone must not look healthy.
func TestUDPL1HostCheck(t *testing.T) {
	silent := udpServer(t, func(req []byte) []byte { return nil })
	saved := hostCheck
	t.Cleanup(func() { hostCheck = saved })
	spec := func(o map[string]string) Spec {
		o["template"] = "syslog"
		return Spec{Type: "UDP", Target: silent, Opts: o, Timeout: 300 * time.Millisecond}
	}
	var asked atomic.Int32
	for _, c := range []struct {
		host   Result
		opts   map[string]string
		vote   Vote
		reason string
		asked  int32
	}{
		{Result{Vote: VoteOK, Reason: ReasonOK}, map[string]string{}, VoteOK, ReasonOK, 1},
		{Result{Vote: VoteFail, Reason: ReasonTimeout}, map[string]string{}, VoteAbstain, ReasonInconclusive, 1},
		{Result{Vote: VoteAbstain, Reason: ReasonPrivilege}, map[string]string{}, VoteAbstain, ReasonInconclusive, 1},
		{Result{Vote: VoteFail, Reason: ReasonHostUnreachable, Detail: "ICMP destination unreachable"}, map[string]string{}, VoteFail, ReasonHostUnreachable, 1},
		{Result{Vote: VoteFail, Reason: ReasonTimeout}, map[string]string{"host-check": "off"}, VoteOK, ReasonOK, 0},
	} {
		asked.Store(0)
		hostCheck = func(context.Context, netip.Addr) Result { asked.Add(1); return c.host }
		want(t, run(t, spec(c.opts)), c.vote, c.reason)
		if asked.Load() != c.asked {
			t.Errorf("host %v %v: host checked %d times", c.host.Reason, c.opts, asked.Load())
		}
	}
	// A closed port is closed whatever the host check says.
	hostCheck = func(context.Context, netip.Addr) Result { return Result{Vote: VoteOK} }
	want(t, run(t, udpSpec(closedUDPPort(t), "syslog", nil)), VoteFail, ReasonPortUnreachable)
	if err := validateUDP(spec(map[string]string{"host-check": "maybe"}), opts{"template": "syslog", "host-check": "maybe"}); err == nil {
		t.Error("host-check=maybe accepted")
	}
}

// The real host check on loopback: the host answers.
func TestUDPL1HostCheckLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r := hostCheck(ctx, netip.MustParseAddr("127.0.0.1")); r.Vote != VoteOK && r.Reason != ReasonPrivilege {
		t.Fatalf("%+v", r)
	}
}

// Only the echo of this request counts; a stray datagram before it is
// set aside.
func TestUDPEchoIgnoresStray(t *testing.T) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			c.WriteTo([]byte("left over from before"), from)
			c.WriteTo(buf[:n], from)
		}
	}()
	want(t, run(t, udpSpec(c.LocalAddr().String(), "echo", nil)), VoteOK, ReasonOK)
}

// A reply that only came after a retransmission may answer the first copy:
// its latency counts from the first send, not from the retransmission.
func TestUDPLatencyAfterRetransmit(t *testing.T) {
	var n atomic.Int32
	addr := udpServer(t, func(req []byte) []byte {
		if n.Add(1) == 1 {
			return nil // the first request is lost
		}
		return req
	})
	r := run(t, udpSpec(addr, "echo", map[string]string{"retransmit": "1"}))
	want(t, r, VoteOK, ReasonOK)
	if r.Latency < 400*time.Millisecond {
		t.Fatalf("latency %v counts from the retransmission", r.Latency)
	}
	// Answered at once: the latency of one request.
	fast := udpServer(t, func(req []byte) []byte { return req })
	if r := run(t, udpSpec(fast, "echo", nil)); r.Latency > 300*time.Millisecond {
		t.Fatalf("latency %v", r.Latency)
	}
}

// dnsReply builds a reply to a query with the given rcode and A answers.
func dnsReply(req []byte, rcode int, aa bool, answers ...[4]byte) []byte {
	resp := append([]byte(nil), req...)
	resp[2] |= 0x80
	if aa {
		resp[2] |= 0x04
	}
	resp[3] = byte(rcode)
	binary.BigEndian.PutUint16(resp[6:], uint16(len(answers)))
	for _, a := range answers {
		resp = append(resp, 0xC0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		resp = append(resp, a[:]...)
	}
	return resp
}

func TestUDPDNS(t *testing.T) {
	var mu sync.Mutex
	var rcode int
	var answers [][4]byte
	set := func(rc int, ans [][4]byte) { mu.Lock(); rcode, answers = rc, ans; mu.Unlock() }
	addr := udpServer(t, func(req []byte) []byte {
		mu.Lock()
		defer mu.Unlock()
		return dnsReply(req, rcode, false, answers...)
	})
	// Liveness mode.
	for rc, wantR := range map[int]string{0: ReasonOK, 3: ReasonOK, 5: ReasonProtoReject, 2: ReasonProtoError} {
		set(rc, nil)
		r := run(t, udpSpec(addr, "dns", nil))
		if r.Reason != wantR {
			t.Errorf("liveness rcode %d: %s (%s)", rc, r.Reason, r.Detail)
		}
	}
	// Content mode.
	set(0, [][4]byte{{10, 0, 0, 53}})
	r := run(t, udpSpec(addr, "dns", map[string]string{"query": "corp.example.com", "expect": "10.0.0.53"}))
	want(t, r, VoteOK, ReasonOK)
	if r.Metrics["dns.answers"] != 1 || len(r.Evidence.Decoded) < 3 {
		t.Fatalf("%+v", r)
	}
	want(t, run(t, udpSpec(addr, "dns", map[string]string{"query": "corp.example.com", "expect": "10.9.9.9"})), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(addr, "dns", map[string]string{"query": "corp.example.com", "aa": "yes"})), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(addr, "dns", map[string]string{"query": "corp.example.com", "expect-all": "10.0.0.53"})), VoteOK, ReasonOK)
	want(t, run(t, udpSpec(addr, "dns", map[string]string{"query": "corp.example.com", "expect-all": "10.0.0.53,10.0.0.54"})), VoteFail, ReasonAssertFailed)
	set(3, nil)
	want(t, run(t, udpSpec(addr, "dns", map[string]string{"query": "gone.example.com"})), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(addr, "dns", map[string]string{"query": "gone.example.com", "rcode": "NXDOMAIN"})), VoteOK, ReasonOK)
	set(2, nil)
	want(t, run(t, udpSpec(addr, "dns", map[string]string{"query": "x.example.com"})), VoteFail, ReasonProtoError)
	// A reply with the wrong id is ignored (spoofing): timeout.
	spoof := udpServer(t, func(req []byte) []byte { r := dnsReply(req, 0, false); r[0] ^= 0xFF; return r })
	want(t, run(t, udpSpec(spoof, "dns", map[string]string{"retransmit": "0"})), VoteFail, ReasonTimeout)
}

func ntpServer(t *testing.T, stratum byte, refid string, li byte, offset time.Duration, echoOrigin bool) string {
	return udpServer(t, func(req []byte) []byte {
		if len(req) < 48 {
			return nil
		}
		resp := make([]byte, 48)
		resp[0] = li<<6 | 4<<3 | 4
		resp[1] = stratum
		copy(resp[12:16], refid)
		now := time.Now().Add(offset)
		if echoOrigin {
			copy(resp[24:32], req[40:48])
		}
		binary.BigEndian.PutUint64(resp[32:], toNTP(now))
		binary.BigEndian.PutUint64(resp[40:], toNTP(now))
		return resp
	})
}

func TestUDPNTP(t *testing.T) {
	r := run(t, udpSpec(ntpServer(t, 2, "\x0a\x00\x00\x01", 0, 0, true), "ntp", nil))
	want(t, r, VoteOK, ReasonOK)
	if r.Metrics["ntp.stratum"] != 2 || r.Metrics["ntp.offset_ms"] > 100 || r.Metrics["ntp.offset_ms"] < -100 {
		t.Fatalf("metrics %v", r.Metrics)
	}
	r = run(t, udpSpec(ntpServer(t, 0, "RATE", 0, 0, true), "ntp", nil))
	if r.Vote != VoteOK || r.Reason != ReasonRemoteRateLimited || !r.Degraded {
		t.Fatalf("KoD RATE: %+v", r)
	}
	want(t, run(t, udpSpec(ntpServer(t, 0, "DENY", 0, 0, true), "ntp", nil)), VoteFail, ReasonAuthError)
	want(t, run(t, udpSpec(ntpServer(t, 16, "", 0, 0, true), "ntp", nil)), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(ntpServer(t, 2, "", 3, 0, true), "ntp", nil)), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(ntpServer(t, 2, "", 0, 5*time.Second, true), "ntp", nil)), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(ntpServer(t, 2, "", 0, 5*time.Second, true), "ntp", map[string]string{"max-offset": "10s"})), VoteOK, ReasonOK)
	// Origin mismatch is ignored like a spoofed reply.
	want(t, run(t, udpSpec(ntpServer(t, 2, "", 0, 0, false), "ntp", map[string]string{"retransmit": "0"})), VoteFail, ReasonTimeout)
}

// snmpAgent answers v1/v2c GET for sysUpTime when the community matches,
// and v3 discovery with a Report.
func snmpAgent(t *testing.T, community string) string {
	return udpServer(t, func(req []byte) []byte {
		msg, _, err := berRead(req)
		if err != nil {
			return nil
		}
		parts, err := berChildren(msg.body, 4)
		if err != nil {
			return nil
		}
		if berInt64(parts[0].body) == 3 {
			hdr, _ := berChildren(parts[1].body, 4)
			usm := tlv(berSeq, tlv(berOctets, []byte{0x80, 0, 0x1f, 0x88, 1, 2, 3, 4}), berInteger(5), berInteger(1234), tlv(berOctets), tlv(berOctets), tlv(berOctets))
			oid, _ := encodeOID("1.3.6.1.6.3.15.1.1.4.0")
			report := tlv(snmpReport, berInteger(1), berInteger(0), berInteger(0), tlv(berSeq, tlv(berSeq, oid, tlv(0x41, []byte{1}))))
			return tlv(berSeq, berInteger(3), tlv(berSeq, hdr[0].tlvBytes(), berInteger(65507), tlv(berOctets, []byte{0}), berInteger(3)), tlv(berOctets, usm), tlv(berSeq, tlv(berOctets), tlv(berOctets), report))
		}
		if string(parts[1].body) != community {
			return nil // wrong community: agents stay silent
		}
		pdu, _ := berChildren(parts[2].body, 4)
		vb, _ := berChildren(pdu[3].body, 4)
		pair, _ := berChildren(vb[0].body, 2)
		oid := tlv(berOID, pair[0].body)
		value := tlv(0x43, []byte{0x01, 0x00})
		if decodeOID(pair[0].body) == "1.3.6.1.2.1.1.1.0" {
			value = tlv(berOctets, []byte("Huawei Versatile Routing Platform"))
		}
		if decodeOID(pair[0].body) == "1.3.6.1.2.1.99.0" {
			value = tlv(0x80)
		}
		resp := tlv(snmpResponse, pdu[0].tlvBytes(), berInteger(0), berInteger(0), tlv(berSeq, tlv(berSeq, oid, value)))
		return tlv(berSeq, parts[0].tlvBytes(), tlv(berOctets, []byte(community)), resp)
	})
}

func (e berElem) tlvBytes() []byte { return tlv(e.tag, e.body) }

func TestUDPSNMP(t *testing.T) {
	addr := snmpAgent(t, "s3cret")
	r := run(t, udpSpec(addr, "snmp", map[string]string{"community": "s3cret"}))
	want(t, r, VoteOK, ReasonOK)
	if r.Metrics["snmp.value"] != 256 {
		t.Fatalf("value %v", r.Metrics)
	}
	if strings.Contains(string(r.Evidence.Request), "s3cret") || strings.Contains(string(r.Evidence.Response), "s3cret") {
		t.Fatal("community leaked into evidence")
	}
	want(t, run(t, udpSpec(addr, "snmp", map[string]string{"community": "s3cret", "oid": "1.3.6.1.2.1.1.1.0", "expect": "Huawei"})), VoteOK, ReasonOK)
	want(t, run(t, udpSpec(addr, "snmp", map[string]string{"community": "s3cret", "oid": "1.3.6.1.2.1.1.1.0", "expect": "Cisco"})), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(addr, "snmp", map[string]string{"community": "s3cret", "expect-num": ">1000"})), VoteFail, ReasonAssertFailed)
	want(t, run(t, udpSpec(addr, "snmp", map[string]string{"community": "s3cret", "oid": "1.3.6.1.2.1.99.0"})), VoteFail, ReasonAssertFailed)
	r = run(t, udpSpec(addr, "snmp", map[string]string{"community": "wrong", "retransmit": "0"}))
	want(t, r, VoteFail, ReasonTimeout)
	if !strings.Contains(r.Detail, "community") {
		t.Fatalf("hint missing: %s", r.Detail)
	}
	r = run(t, udpSpec(addr, "snmp", map[string]string{"version": "3-discovery"}))
	want(t, r, VoteOK, ReasonOK)
	if !strings.Contains(r.Detail, "80001f88") {
		t.Fatalf("engine id: %s", r.Detail)
	}
}

func TestUDPTFTP(t *testing.T) {
	// The server answers from a different port, as real TFTP servers do.
	c, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer c.Close()
	var replyCode atomic.Int32
	replyCode.Store(5)
	go func() {
		buf := make([]byte, 600)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 2 || buf[1] != 1 {
				continue
			}
			other, _ := net.ListenPacket("udp", "127.0.0.1:0")
			if replyCode.Load() == 5 {
				other.WriteTo(append([]byte{0, 5, 0, 1}, "File not found\x00"...), from)
			} else {
				other.WriteTo([]byte{0, 3, 0, 1, 'h', 'i'}, from)
			}
			other.Close()
		}
	}()
	addr := c.LocalAddr().String()
	want(t, run(t, udpSpec(addr, "tftp", nil)), VoteOK, ReasonProtoReject)
	want(t, run(t, udpSpec(addr, "tftp", map[string]string{"expect": "data", "file": "startup.cfg"})), VoteFail, ReasonAssertFailed)
	replyCode.Store(3)
	want(t, run(t, udpSpec(addr, "tftp", map[string]string{"expect": "data", "file": "startup.cfg"})), VoteOK, ReasonOK)
}

func TestUDPSunRPC(t *testing.T) {
	addr := udpServer(t, func(req []byte) []byte {
		if len(req) < 40 {
			return nil
		}
		resp := binary.BigEndian.AppendUint32(nil, binary.BigEndian.Uint32(req))
		for _, v := range []uint32{1, 0, 0, 0, 0} { // REPLY, MSG_ACCEPTED, verf AUTH_NULL, SUCCESS
			resp = binary.BigEndian.AppendUint32(resp, v)
		}
		if binary.BigEndian.Uint32(req[20:]) == 3 { // GETPORT
			port := uint32(0)
			if binary.BigEndian.Uint32(req[40:]) == 100003 {
				port = 2049
			}
			resp = binary.BigEndian.AppendUint32(resp, port)
		}
		return resp
	})
	want(t, run(t, udpSpec(addr, "sunrpc", nil)), VoteOK, ReasonOK)
	want(t, run(t, udpSpec(addr, "sunrpc", map[string]string{"expect-program": "nfs"})), VoteOK, ReasonOK)
	want(t, run(t, udpSpec(addr, "sunrpc", map[string]string{"expect-program": "mountd"})), VoteFail, ReasonAssertFailed)
}

func TestUDPRaw(t *testing.T) {
	addr := udpServer(t, func(req []byte) []byte { return append([]byte("ACK "), req...) })
	want(t, run(t, Spec{Type: "UDP", Target: addr, Opts: map[string]string{"send": "hello", "expect": "ACK hello"}, Timeout: time.Second}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "UDP", Target: addr, Opts: map[string]string{"send-hex": "01{{nonce4}}", "expect-len": "9"}, Timeout: time.Second}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "UDP", Target: addr, Opts: map[string]string{"send": "x", "expect-regex": "^NAK"}, Timeout: time.Second}), VoteFail, ReasonAssertFailed)
	want(t, run(t, Spec{Type: "UDP", Target: addr, Opts: map[string]string{"send": "x", "expect": "any", "accept-from": "any-port"}, Timeout: time.Second}), VoteOK, ReasonOK)
}

func TestUDPTime(t *testing.T) {
	addr := udpServer(t, func(req []byte) []byte {
		secs := uint32(time.Since(ntpEpoch) / time.Second)
		return binary.BigEndian.AppendUint32(nil, secs)
	})
	want(t, run(t, udpSpec(addr, "time", nil)), VoteOK, ReasonOK)
	bad := udpServer(t, func(req []byte) []byte { return []byte{0, 0, 0, 1} })
	want(t, run(t, udpSpec(bad, "time", nil)), VoteFail, ReasonProtoError)
}

func TestL0Keyword(t *testing.T) {
	r := run(t, Spec{Type: "UDP", Target: "127.0.0.1:nameserver"})
	if r.Vote != VoteAbstain || r.Depth != keywords.L0 {
		t.Fatalf("%+v", r)
	}
}

func TestRunNeverPanics(t *testing.T) {
	for _, s := range []Spec{{}, {Type: "UDP"}, {Type: "HTTP", Target: "::"}, {Type: "PING", Target: ""}} {
		r := run(t, s)
		if r.Vote == "" {
			t.Fatalf("%+v → %+v", s, r)
		}
	}
}

func TestSecretsScrubbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", r.Header.Get("X-Api"))
		fmt.Fprintf(w, "token=%s", r.URL.Query().Get("t"))
	}))
	defer srv.Close()
	secret := "s3cr3t/value+x"
	r := run(t, Spec{Type: "HTTP", Target: srv.URL + "/?t=" + url.QueryEscape(secret), Opts: map[string]string{"header.X-Api": secret}, Secrets: []string{secret}})
	if r.Vote != VoteOK || r.Evidence == nil {
		t.Fatalf("%+v", r)
	}
	all := string(r.Evidence.Request) + string(r.Evidence.Response) + r.Detail
	if strings.Contains(all, secret) || strings.Contains(all, url.QueryEscape(secret)) || !strings.Contains(all, "***") {
		t.Fatalf("secret recorded:\n%s", all)
	}
}

// Checks with the same fixed source port take turns instead of failing to
// bind; one that would have no time left, or a port another program holds,
// leaves the check undecided rather than failing the target.
func TestUDPSourcePortTurns(t *testing.T) {
	free := func() string {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return strconv.Itoa(c.LocalAddr().(*net.UDPAddr).Port)
	}
	slow := udpServer(t, func(req []byte) []byte { time.Sleep(200 * time.Millisecond); return req })
	raw := func(addr, port string, timeout time.Duration) Spec {
		return Spec{Type: "UDP", Target: addr, Timeout: timeout, Opts: map[string]string{"template": "raw", "src-port": port, "retransmit": "0"}}
	}
	port := free()
	var wg sync.WaitGroup
	results := make([]Result, 3)
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = Run(context.Background(), raw(slow, port, 2*time.Second)) }()
	}
	wg.Wait()
	for i, r := range results {
		if r.Vote != VoteOK {
			t.Errorf("check %d with a shared source port: %+v", i, r)
		}
	}
	// The holder takes all of its time: the other one gives up undecided.
	silent := udpServer(t, func([]byte) []byte { return nil })
	port = free()
	done := make(chan Result, 1)
	go func() { done <- Run(context.Background(), raw(silent, port, 600*time.Millisecond)) }()
	time.Sleep(50 * time.Millisecond)
	want(t, Run(context.Background(), raw(slow, port, 600*time.Millisecond)), VoteAbstain, ReasonPortBusy)
	want(t, <-done, VoteFail, ReasonTimeout)
	// Another program has the port.
	hold, err := net.ListenPacket("udp", ":"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if runtime.GOOS == "windows" {
		t.Skip("a port held by another program may not be detected on Windows (documented as possibly unsupported)")
	}
	want(t, Run(context.Background(), raw(slow, port, time.Second)), VoteAbstain, ReasonPortBusy)
	if !LocalAbstain(ReasonPortBusy) {
		t.Error("a busy source port says nothing about the target")
	}
}
