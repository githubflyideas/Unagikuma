package probe

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDomainExpiryRDAP(t *testing.T) {
	expires := map[string]time.Time{
		"far.test":  time.Now().Add(400 * 24 * time.Hour),
		"soon.test": time.Now().Add(20 * 24 * time.Hour),
		"edge.test": time.Now().Add(3 * 24 * time.Hour),
		"gone.test": time.Now().Add(-2 * 24 * time.Hour),
	}
	rdap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/rdap/domain/")
		exp, ok := expires[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"objectClassName":"domain","events":[{"eventAction":"registration","eventDate":"2010-01-01T00:00:00Z"},{"eventAction":"expiration","eventDate":%q}]}`, exp.UTC().Format(time.RFC3339))
	}))
	defer rdap.Close()
	boot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"services":[[["test"],[%q]]]}`, rdap.URL+"/rdap/")
	}))
	defer boot.Close()
	defer func(u, w string) { rdapBootstrapURL, ianaWhois = u, w }(rdapBootstrapURL, ianaWhois)
	rdapBootstrapURL, ianaWhois = boot.URL, "127.0.0.1:1" // no internet: the WHOIS fallback fails at once
	bootstrap.Lock()
	bootstrap.services = nil
	bootstrap.Unlock()

	for _, c := range []struct {
		name     string
		vote     Vote
		reason   string
		degraded bool
	}{
		{"far.test", VoteOK, ReasonOK, false},
		{"soon.test", VoteOK, ReasonOK, true},
		{"edge.test", VoteFail, ReasonExpiring, false},
		{"gone.test", VoteFail, ReasonExpiring, false},
		{"unknown.test", VoteAbstain, ReasonRegistry, false},
	} {
		r := Run(context.Background(), Spec{Name: c.name, Type: "DOMAIN", Target: c.name, Timeout: 5 * time.Second})
		if r.Vote != c.vote || r.Reason != c.reason || r.Degraded != c.degraded {
			t.Errorf("%s: %+v", c.name, r)
		}
		if c.vote != VoteAbstain && r.Metrics["domain.days"] == 0 {
			t.Errorf("%s: no days metric", c.name)
		}
	}
}

func TestDomainExpiryWHOIS(t *testing.T) {
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
			q, _ := bufio.NewReader(c).ReadString('\n')
			if strings.TrimSpace(q) == "example.cn" {
				fmt.Fprint(c, "Domain Name: example.cn\r\nRegistrant: x\r\nExpiration Time: 2099-03-01 12:00:00\r\n")
			}
			c.Close()
		}
	}()
	r := Run(context.Background(), Spec{Name: "cn", Type: "DOMAIN", Target: "example.cn", Timeout: 5 * time.Second, Opts: map[string]string{"whois": l.Addr().String()}})
	if r.Vote != VoteOK || r.Metrics["domain.days"] < 365 {
		t.Fatalf("%+v", r)
	}
	for _, bad := range []Spec{
		{Type: "DOMAIN", Target: "not a domain"},
		{Type: "DOMAIN", Target: "example.com", Opts: map[string]string{"days": "5", "critical-days": "10"}},
		{Type: "DOMAIN", Target: "example.com", Opts: map[string]string{"color": "red"}},
	} {
		if Validate(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
