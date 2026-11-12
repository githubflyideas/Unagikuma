package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

// Only the fixed candidates are measured, the two best that answer are
// suggested, and a request soon after gets the same result.
func TestExitSuggest(t *testing.T) {
	rtt := map[string]time.Duration{"114.114.114.114": 8 * time.Millisecond, "223.5.5.5": 9 * time.Millisecond, "8.8.8.8": 180 * time.Millisecond}
	var asked []string
	var mu sync.Mutex
	prev, prevPub := exitProbe, exitPublic
	t.Cleanup(func() { exitProbe, exitPublic, exitLastOut = prev, prevPub, nil })
	exitLastOut = nil
	exitProbe = func(_ context.Context, sp probe.Spec) probe.Result {
		mu.Lock()
		asked = append(asked, sp.Target)
		mu.Unlock()
		if sp.Type == "TCP" {
			if sp.Target == "9.9.9.9:53" {
				return probe.Result{Vote: probe.VoteOK, Latency: 50 * time.Millisecond}
			}
			return probe.Result{Vote: probe.VoteFail}
		}
		if d, ok := rtt[sp.Target]; ok {
			return probe.Result{Vote: probe.VoteOK, Latency: d, Metrics: map[string]float64{"ping.loss": 0}}
		}
		return probe.Result{Vote: probe.VoteFail, Detail: "no reply"}
	}
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("203.0.113.7\n")) }))
	defer pub.Close()
	exitPublic = pub.URL
	s := &Server{}
	out, err := s.exitSuggest(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	best := m["suggested"].([]string)
	if len(best) != 2 || best[0] != "114.114.114.114" || best[1] != "223.5.5.5" || m["public_addr"] != "203.0.113.7" {
		t.Fatalf("suggested %v, public %v", best, m["public_addr"])
	}
	known := map[string]bool{}
	for _, c := range exitCandidates {
		known[c.Addr] = true
	}
	for _, a := range asked {
		if !known[strings.TrimSuffix(a, ":53")] {
			t.Fatalf("measured %s, which is not a candidate", a)
		}
	}
	// Ping filtered, port 53 answers: measured, and said so.
	for _, c := range m["candidates"].([]exitMeasure) {
		if c.Addr == "9.9.9.9" && (!c.OK || c.Via != "tcp/53") {
			t.Fatalf("9.9.9.9: %+v", c)
		}
	}
	n := len(asked)
	s.exitSuggest(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	if len(asked) != n {
		t.Fatal("a second request within the gap measured again")
	}
	// A public-address service that answers something else is ignored.
	pub2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("<html>evil</html>")) }))
	defer pub2.Close()
	exitPublic = pub2.URL
	if got := publicAddr(context.Background()); got != "" {
		t.Fatalf("public address %q from a non-IP answer", got)
	}
}
