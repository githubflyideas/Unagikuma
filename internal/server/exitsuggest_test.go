package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

func fakeExit(t *testing.T, rtt map[string]time.Duration, hops []probe.Hop, pub, cc string) *[]string {
	t.Helper()
	var asked []string
	var mu sync.Mutex
	prevP, prevT, prevPub, prevWhere := exitProbe, exitTrace, exitPublic, exitWhere
	t.Cleanup(func() {
		exitProbe, exitTrace, exitPublic, exitWhere = prevP, prevT, prevPub, prevWhere
		exitLastOut, exitLastAt = map[string]map[string]any{}, map[string]time.Time{}
	})
	exitLastOut, exitLastAt = map[string]map[string]any{}, map[string]time.Time{}
	exitProbe = func(_ context.Context, sp probe.Spec) probe.Result {
		mu.Lock()
		asked = append(asked, sp.Target)
		mu.Unlock()
		if d, ok := rtt[sp.Target]; ok {
			return probe.Result{Vote: probe.VoteOK, Latency: d, Metrics: map[string]float64{"ping.loss": 0}}
		}
		return probe.Result{Vote: probe.VoteFail}
	}
	exitTrace = func(context.Context, netip.Addr, int, time.Duration) ([]probe.Hop, error) { return hops, nil }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "country") {
			w.Write([]byte(cc + "\n"))
			return
		}
		w.Write([]byte(pub + "\n"))
	}))
	t.Cleanup(srv.Close)
	exitPublic, exitWhere = srv.URL+"/ip", srv.URL+"/country-iso"
	return &asked
}

func suggest(t *testing.T, body map[string]string) (map[string]any, error) {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(b))
	out, err := (&Server{}).exitSuggest(httptest.NewRecorder(), r)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

// By city: only that city's listed resolvers are measured, best first;
// a city not on the list is refused.
func TestExitSuggestByCity(t *testing.T) {
	asked := fakeExit(t, map[string]time.Duration{"223.5.5.5": 7 * time.Millisecond, "223.6.6.6:53": 9 * time.Millisecond}, nil, "", "")
	m, err := suggest(t, map[string]string{"mode": "city", "city": "hangzhou"})
	if err != nil {
		t.Fatal(err)
	}
	if got := m["suggested"].([]string); len(got) != 2 || got[0] != "223.5.5.5" || got[1] != "223.6.6.6" {
		t.Fatalf("suggested %v", got)
	}
	for _, a := range *asked {
		if !strings.HasPrefix(a, "223.5.5.5") && !strings.HasPrefix(a, "223.6.6.6") {
			t.Fatalf("measured %s, not a Hangzhou resolver", a)
		}
	}
	if _, err := suggest(t, map[string]string{"mode": "city", "city": "10.0.0.1"}); err == nil {
		t.Fatal("an unlisted city was measured")
	}
}

// Every city has a name in every language, and addresses that parse.
func TestExitCitiesNamed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range exitCities {
		if seen[c.ID] || len(c.Addrs) == 0 {
			t.Errorf("%s: duplicate or empty", c.ID)
		}
		seen[c.ID] = true
		for _, a := range c.Addrs {
			if _, err := netip.ParseAddr(a); err != nil {
				t.Errorf("%s: %s", c.ID, a)
			}
		}
		for _, l := range []string{"en", "zh", "ar", "ja"} {
			if k := "ui.city." + c.ID; i18n.T(l, k) == k {
				t.Errorf("%s: no name in %s", c.ID, l)
			}
		}
	}
}

// Automatic: the country's best resolver and the first public router of
// the route are proposed; private and shared (CGNAT) hops are skipped.
func TestExitSuggestAutomatic(t *testing.T) {
	hops := []probe.Hop{{TTL: 1, Addr: "192.168.1.1"}, {TTL: 2, Addr: "100.64.0.1"}, {TTL: 3}, {TTL: 4, Addr: "61.135.1.1"}, {TTL: 5, Addr: "202.97.1.1"}}
	asked := fakeExit(t, map[string]time.Duration{"114.114.114.114": 20 * time.Millisecond, "223.5.5.5": 9 * time.Millisecond, "61.135.1.1": 3 * time.Millisecond, "202.97.1.1": 6 * time.Millisecond},
		hops, "203.0.113.9", "CN")
	m, err := suggest(t, map[string]string{"mode": "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if got := m["suggested"].([]string); len(got) != 2 || got[0] != "223.5.5.5" || got[1] != "61.135.1.1" {
		t.Fatalf("suggested %v", got)
	}
	if m["public_addr"] != "203.0.113.9" || m["country"] != "CN" {
		t.Fatalf("public %v country %v", m["public_addr"], m["country"])
	}
	for _, a := range *asked {
		if strings.HasPrefix(a, "192.168.") || strings.HasPrefix(a, "100.64.") {
			t.Fatalf("measured the private hop %s", a)
		}
	}
	// What the services answer is used only when it is an address or a code.
	if parseIP("<html>") != "" || parseCountry("china") != "" || parseCountry("CN") != "CN" {
		t.Fatal("answers are not checked")
	}
}

// Nothing answers: an empty list, not null (the page reads its length).
func TestExitSuggestNothingAnswers(t *testing.T) {
	fakeExit(t, nil, nil, "", "")
	m, err := suggest(t, map[string]string{"mode": "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(m["suggested"]); string(b) != "[]" {
		t.Fatalf("suggested %s", b)
	}
}
