package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
)

// A name that resolves to IPv6 first while the service listens on IPv4
// only (localhost on many systems) is still reached, as curl reaches it.
func TestDialFallsBackToTheOtherFamily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	defer func(f func(context.Context, string, string) ([]netip.Addr, error)) { lookupNetIP = f }(lookupNetIP)
	lookupNetIP = func(_ context.Context, network, _ string) ([]netip.Addr, error) {
		switch network {
		case "ip6":
			return []netip.Addr{netip.MustParseAddr("::1")}, nil
		case "ip4":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	// Make sure nothing answers on ::1 at that port, or the test proves nothing.
	if c, err := net.Dial("tcp", "[::1]:"+port); err == nil {
		c.Close()
		t.Skip("something listens on [::1]:" + port)
	}
	want(t, run(t, Spec{Type: "HTTP", Target: "http://dualstack.test:" + port + "/"}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "TCP", Target: "dualstack.test:" + port}), VoteOK, ReasonOK)
	// family= pins the family: no fallback then.
	r := run(t, Spec{Type: "TCP", Target: "dualstack.test:" + port, Opts: map[string]string{"family": "6"}})
	if r.Vote != VoteFail {
		t.Fatalf("family=6 fell back to IPv4: %+v", r)
	}
}

// Ten redirects are followed, as the message says; the eleventh fails.
func TestRedirectLimitIsTen(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r/"))
		if n <= 0 {
			w.Write([]byte("done"))
			return
		}
		http.Redirect(w, r, "/r/"+strconv.Itoa(n-1), http.StatusFound)
	}))
	defer srv.Close()
	want(t, run(t, Spec{Type: "HTTP", Target: srv.URL + "/r/10"}), VoteOK, ReasonOK)
	want(t, run(t, Spec{Type: "HTTP", Target: srv.URL + "/r/11"}), VoteFail, ReasonProtoError)
}
