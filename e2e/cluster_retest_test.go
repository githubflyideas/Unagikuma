//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ensure signs in again when the session is gone (a restart, a promote).
func (s *server) ensure() {
	resp, err := s.c.Get(s.url("/api/v1/system"))
	if err != nil {
		return
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		for _, u := range [][2]string{{"local", "standby-pass-1"}, {"admin", "master-pass-1"}, {"local", "node-pass-1"}} {
			if s.loginQuiet(u[0], u[1]) {
				return
			}
		}
	}
}

func (s *server) loginQuiet(user, pass string) bool {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := c.Post(s.url("/api/v1/session"), "application/json", bytes.NewReader(b))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var r struct{ CSRF string }
	_ = json.NewDecoder(resp.Body).Decode(&r)
	s.c.Jar, s.csrf = jar, r.CSRF
	return true
}

// soaRT is soa with room for the test's own sign-ins after restarts.
const soaRT = soa + "$SET api.login-rate=100/s api.rate=200/s\n"

type sysInfo struct {
	Term       uint64
	Superseded *struct{ Term uint64 }
}

func (s *server) sys() sysInfo {
	s.ensure()
	var r sysInfo
	s.call("GET", "/api/v1/overview", nil, &r)
	return r
}

type agentInfo struct {
	Member *struct{ Node, Server string }
	Status *struct{ Connected bool }
}

func (s *server) agent() agentInfo {
	s.ensure()
	var r agentInfo
	s.call("GET", "/api/v1/cluster/agent", nil, &r)
	return r
}

// isMaster: not a member and not replaced.
func (s *server) isMaster() bool {
	if s.agent().Member != nil {
		return false
	}
	var r struct{ Mode string }
	return s.call("GET", "/api/v1/config", nil, &r) == 200 && r.Mode == "rw" && s.sys().Superseded == nil
}

func (s *server) joined(master *server, node string) bool {
	ag := s.agent()
	return ag.Member != nil && ag.Member.Node == node && ag.Status != nil && ag.Status.Connected &&
		strings.HasSuffix(ag.Member.Server, fmt.Sprintf(":%d", master.port))
}

func (s *server) logHas(sub string) bool { return strings.Contains(s.log.String(), sub) }

func (s *server) logCount(sub string) int { return strings.Count(s.log.String(), sub) }

// join adds s to master as node, and makes it a standby with a state copy.
func join(t *testing.T, master, s *server, node string) {
	t.Helper()
	var jc struct{ Code string }
	if code := master.call("POST", "/api/v1/cluster/join-codes", map[string]string{"server": fmt.Sprintf("https://127.0.0.1:%d", master.port)}, &jc); code != 200 {
		t.Fatalf("join code: %d", code)
	}
	if code := s.call("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": node, "title": node}, nil); code != 200 {
		t.Fatalf("%s join: %d", node, code)
	}
	master.waitFor(node+" online", func() bool { return nodeOnline(master, node) })
}

func nodeOnline(master *server, node string) bool {
	var r struct {
		Nodes []struct {
			Name   string
			Online bool
		}
	}
	master.call("GET", "/api/v1/nodes", nil, &r)
	for _, n := range r.Nodes {
		if n.Name == node && n.Online {
			return true
		}
	}
	return false
}

func makeStandby(t *testing.T, master, s *server, node string) {
	t.Helper()
	t0 := time.Now().Add(-time.Second)
	if code := master.call("PUT", "/api/v1/nodes/"+node, map[string]any{"title": node, "standby": fmt.Sprintf("https://127.0.0.1:%d", s.port)}, nil); code != 200 {
		t.Fatalf("set standby %s: %d", node, code)
	}
	waitLong(t, s.name+" to copy the state", 90*time.Second, func() bool {
		s.ensure()
		var r struct {
			Standby struct {
				Size    int64
				Fetched time.Time
			}
		}
		s.call("GET", "/api/v1/cluster/agent", nil, &r)
		return r.Standby.Size > 0 && r.Standby.Fetched.After(t0)
	})
}

func waitLong(t *testing.T, what string, d time.Duration, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > d {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(time.Second)
	}
	return time.Since(start)
}

// Two standbys promoted at the same moment while the master is gone: one
// takes over, the other is refused; there is never a second master.
func TestRetestConcurrentPromote(t *testing.T) {
	rounds := 1 // UNAGIKUMA_E2E_ROUNDS=5 repeats the race
	if n, err := strconv.Atoi(os.Getenv("UNAGIKUMA_E2E_ROUNDS")); err == nil && n > 0 {
		rounds = n
	}
	for round := 1; round <= rounds; round++ {
		t.Run(fmt.Sprintf("round%d", round), func(t *testing.T) {
			a := start(t, "a", fmt.Sprintf(soaRT, "a"))
			a.setup("admin", "master-pass-1")
			b := start(t, "b", fmt.Sprintf(soaRT, "b"))
			b.setup("local", "standby-pass-1")
			c := start(t, "c", fmt.Sprintf(soaRT, "c"))
			c.setup("local", "standby-pass-1")
			join(t, a, b, "bk")
			join(t, a, c, "ck")
			makeStandby(t, a, b, "bk")
			makeStandby(t, a, c, "ck")
			b.waitFor("ck known as a standby", func() bool {
				var r struct{ Text string }
				b.call("GET", "/api/v1/config", nil, &r)
				return strings.Contains(r.Text, fmt.Sprintf("127.0.0.1:%d", c.port))
			})

			a.stop()
			for _, s := range []*server{b, c} {
				waitLong(t, s.name+" to lose the master", 60*time.Second, func() bool {
					ag := s.agent()
					return ag.Status == nil || !ag.Status.Connected
				})
			}
			var wg sync.WaitGroup
			codes := map[string]int{}
			var mu sync.Mutex
			for _, s := range []*server{b, c} {
				wg.Add(1)
				go func(s *server) {
					defer wg.Done()
					code := s.call("POST", "/api/v1/cluster/promote", map[string]any{}, nil)
					mu.Lock()
					codes[s.name] = code
					mu.Unlock()
				}(s)
			}
			wg.Wait()
			t.Logf("promote answers: %v", codes)
			ok := 0
			for _, c := range codes {
				if c == 200 {
					ok++
				}
			}
			if ok != 1 {
				t.Fatalf("promotes that went ahead: %d (%v), want exactly 1", ok, codes)
			}
			// Watch for 30 s: never two masters.
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if b.isMaster() && c.isMaster() {
					t.Fatal("two masters")
				}
				time.Sleep(500 * time.Millisecond)
			}
			masters := 0
			for _, s := range []*server{b, c} {
				if s.isMaster() {
					masters++
				}
				if s.logHas("Two masters") || s.logHas("another master has") {
					t.Errorf("%s saw a second master", s.name)
				}
			}
			if masters != 1 {
				t.Fatalf("masters after 30 s: %d", masters)
			}
		})
	}
}

// a → b → a by handover, then a is lost and b promotes: b takes a term
// above 2, and keeps the role when a comes back.
func TestRetestPromoteAfterHandoverRoundTrip(t *testing.T) {
	// killed: the master's process is gone. frozen: it hangs (SIGSTOP), as
	// in a network partition, and comes back as it was.
	for _, freeze := range []bool{false, true} {
		name := map[bool]string{false: "killed", true: "frozen"}[freeze]
		t.Run(name, func(t *testing.T) { promoteAfterRoundTrip(t, freeze) })
	}
}

func promoteAfterRoundTrip(t *testing.T, freeze bool) {
	a := start(t, "a", fmt.Sprintf(soaRT, "a"))
	a.setup("admin", "master-pass-1")
	b := start(t, "b", fmt.Sprintf(soaRT, "b"))
	b.setup("local", "standby-pass-1")
	join(t, a, b, "bk")
	makeStandby(t, a, b, "bk")

	if code := a.call("POST", "/api/v1/nodes/bk/handover", map[string]any{}, nil); code != 200 {
		t.Fatalf("handover a→b: %d", code)
	}
	waitLong(t, "b master at term 1", 90*time.Second, func() bool { return b.isMaster() && b.sys().Term == 1 })
	b.login("local", "standby-pass-1")
	waitLong(t, "a to follow b", 90*time.Second, func() bool { return a.joined(b, "a") })
	makeStandby(t, b, a, "a")

	if code := b.call("POST", "/api/v1/nodes/a/handover", map[string]any{}, nil); code != 200 {
		t.Fatalf("handover b→a: %d", code)
	}
	waitLong(t, "a master at term 2", 90*time.Second, func() bool { return a.isMaster() && a.sys().Term == 2 })
	a.login("admin", "master-pass-1")
	waitLong(t, "b to follow a", 90*time.Second, func() bool { return b.joined(a, "bk") })
	makeStandby(t, a, b, "bk")

	if freeze {
		_ = a.cmd.Process.Signal(syscall.SIGSTOP)
	} else {
		a.stop()
	}
	var pr struct{ Term uint64 }
	// A frozen master still holds its connections for a while: promote
	// is refused (master_alive) until the member gives up on it.
	waitLong(t, "b's promote to go ahead", 150*time.Second, func() bool {
		return b.call("POST", "/api/v1/cluster/promote", map[string]any{}, &pr) == 200
	})
	t.Logf("b promoted to term %d", pr.Term)
	if pr.Term <= 2 {
		t.Fatalf("promote term %d, want above 2", pr.Term)
	}
	waitLong(t, "b master", 60*time.Second, func() bool { return b.isMaster() })
	b.login("local", "standby-pass-1")

	if freeze {
		_ = a.cmd.Process.Signal(syscall.SIGCONT)
	} else {
		a.cmd = nil
		a.run()
	}
	a.login("admin", "master-pass-1")
	d := waitLong(t, "a to rejoin b", 120*time.Second, func() bool { return a.joined(b, "a") })
	t.Logf("a rejoined b after %s", d.Round(100*time.Millisecond))
	if !b.isMaster() || b.sys().Term != pr.Term {
		t.Fatalf("b lost the role: master=%v term=%d", b.isMaster(), b.sys().Term)
	}
}

// The old master comes back while the new one refuses its rejoin request
// (rate limit). It keeps retrying, in the same process and after a
// restart, and joins once the limit lets it through.
func TestRetestRejoinRetriesAfterRefusal(t *testing.T) {
	// early: the master is lost right after the standby is set, and
	// learns it was replaced from the new master's notice. settled: it ran
	// a while first, and finds the new master itself when it starts.
	for _, tc := range []struct {
		name string
		wait time.Duration
		path string
	}{{"early", 0, "could not rejoin the new master as a replica"}, {"settled", 8 * time.Second, "stepping down at start"}} {
		t.Run(tc.name, func(t *testing.T) { rejoinAfterRefusal(t, tc.wait, tc.path) })
	}
}

func rejoinAfterRefusal(t *testing.T, wait time.Duration, path string) {
	a := start(t, "a", fmt.Sprintf(soaRT, "a"))
	a.setup("admin", "master-pass-1")
	b := start(t, "b", fmt.Sprintf(soaRT, "b"))
	b.setup("local", "standby-pass-1")
	join(t, a, b, "bk")
	makeStandby(t, a, b, "bk")
	time.Sleep(wait)
	a.stop()
	if code := b.call("POST", "/api/v1/cluster/promote", map[string]any{}, nil); code != 200 {
		t.Fatalf("promote: %d", code)
	}
	waitLong(t, "b master", 60*time.Second, func() bool { return b.isMaster() })
	b.login("local", "standby-pass-1")

	// Keep b's limit for unauthenticated cluster requests from 127.0.0.1
	// used up.
	stop := make(chan struct{})
	var flood sync.WaitGroup
	for range 4 {
		flood.Add(1)
		go func() {
			defer flood.Done()
			cl := &http.Client{Timeout: 2 * time.Second}
			for {
				select {
				case <-stop:
					return
				default:
				}
				if resp, err := cl.Post(b.url("/api/v1/agent/rejoin-offer"), "application/json", strings.NewReader("{}")); err == nil {
					resp.Body.Close()
				}
			}
		}()
	}

	refusals := func() int {
		return a.logCount("could not obtain rejoin offer") + a.logCount("could not rejoin the new master as a replica")
	}
	a.cmd = nil
	a.run()
	waitLong(t, "a's first rejoin to be refused", 90*time.Second, func() bool { return refusals() > 0 })
	time.Sleep(35 * time.Second)
	n := refusals()
	t.Logf("refused rejoins in the first process: %d", n)
	if n < 2 {
		t.Fatalf("the first process did not retry (%d attempts)", n)
	}
	if a.agent().Member != nil {
		t.Fatal("joined while refused?")
	}
	if !a.logHas(path) {
		t.Errorf("a did not take the expected path (%q)", path)
	}

	// Restart while still refused, then let it through.
	_ = a.cmd.Process.Signal(syscall.SIGKILL)
	_ = a.cmd.Wait()
	a.cmd = nil
	a.run()
	waitLong(t, "a rejoin attempt after the restart", 60*time.Second, func() bool { return refusals() > 0 })
	t.Logf("refused rejoins after the restart: %d", refusals())
	close(stop)
	flood.Wait()
	d := waitLong(t, "a to rejoin b", 40*time.Second, func() bool { return a.joined(b, "a") })
	t.Logf("a rejoined b %s after the refusals stopped", d.Round(100*time.Millisecond))
	resp, err := a.c.Get(a.url("/healthz"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz %d after rejoining", resp.StatusCode)
	}
	if !b.isMaster() {
		t.Fatal("b is no longer master")
	}
}

// Several machines on one address join one after another without pause.
func TestRetestJoinsFromOneAddress(t *testing.T) {
	// The default limits: this is about them.
	a := start(t, "a", fmt.Sprintf(soa, "a"))
	a.setup("admin", "master-pass-1")
	var nodes []*server
	for i := range 6 {
		s := start(t, fmt.Sprintf("n%d", i), fmt.Sprintf(soa, fmt.Sprintf("n%d", i)))
		s.setup("local", "node-pass-1")
		nodes = append(nodes, s)
	}
	for i, s := range nodes {
		var jc struct{ Code string }
		if code := a.call("POST", "/api/v1/cluster/join-codes", map[string]string{"server": fmt.Sprintf("https://127.0.0.1:%d", a.port)}, &jc); code != 200 {
			t.Fatalf("join code: %d", code)
		}
		if code := s.call("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": fmt.Sprintf("n%d", i)}, nil); code != 200 {
			t.Fatalf("join %d refused: %d", i, code)
		}
	}
	for i := range nodes {
		a.waitFor("node online", func() bool { return nodeOnline(a, fmt.Sprintf("n%d", i)) })
	}
}

// A hand-written file with revision=1 can be edited in the console.
func TestRetestCountedRevisionEditable(t *testing.T) {
	a := start(t, "a", "$FILE revision=1 master=a\nhello IN HTTP https://127.0.0.1:9/\n")
	a.setup("admin", "master-pass-1")
	for i := range 3 {
		if code := a.call("POST", "/api/v1/monitors", map[string]any{"record": map[string]string{
			"name": fmt.Sprintf("m%d", i), "type": "TCP", "target": fmt.Sprintf("127.0.0.1:%d", a.port)}}, nil); code != 200 {
			t.Fatalf("edit %d: %d", i, code)
		}
	}
	var r struct {
		Serial uint64
		Text   string
	}
	a.call("GET", "/api/v1/config", nil, &r)
	t.Logf("revision after three edits: %d", r.Serial)
	if !strings.Contains(r.Text, "m2") {
		t.Fatal("edits not in the file")
	}
}
