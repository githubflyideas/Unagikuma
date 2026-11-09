package server

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/cluster"
)

// tlsEnv serves the handler on a listener that also speaks TLS, like the
// real server, so agents and joining machines can reach it.
func tlsEnv(t *testing.T, bind string) *env {
	t.Helper()
	e := newEnv(t, bind, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(e.srv.Handler())
	ts.Listener = cluster.SniffListener(ln, e.app.PKI().TLSConfig(nil), time.Second)
	ts.Start()
	t.Cleanup(ts.Close)
	e.ts.Close()
	e.ts = ts
	e.setup()
	return e
}

const fastLogin = "$SET api.login-rate=100/s\n"

func TestJoinWithCode(t *testing.T) {
	master := tlsEnv(t, testBind+fastLogin)
	var jc struct {
		Code   string `json:"code"`
		Server string `json:"server"`
	}
	if master.do("POST", "/api/v1/cluster/join-codes", map[string]any{}, &jc) != 200 || !strings.HasPrefix(jc.Code, "nkjoin1.") {
		t.Fatalf("join code: %+v", jc)
	}
	joiner := tlsEnv(t, `@ IN SOA box. admin.localhost. ( 2026102801 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s
oncall  IN CHANNEL log
pager   IN POLICY chain=oncall
erp     IN HTTP http://10.9.9.5/ title="ERP" policy=pager
web     IN TCP 10.9.9.6:443
sw      IN PING 10.9.9.1
`)
	var res struct {
		Node     string   `json:"node"`
		Imported []string `json:"imported"`
		Skipped  string   `json:"skipped"`
	}
	if c := joiner.do("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": "bj", "title": "北京", "region": "north", "import": true}, &res); c != 200 {
		t.Fatalf("join: %d", c)
	}
	// "web" exists on the master already, so it comes in prefixed.
	if res.Node != "bj" || strings.Join(res.Imported, ",") != "erp,bj-web,sw" || res.Skipped != "" {
		t.Fatalf("join result: %+v", res)
	}
	mc := master.app.Config().Config
	if n := mc.FindNode("bj"); n == nil || n.Region != "north" || n.Description != "北京" {
		t.Fatalf("node record: %+v", n)
	}
	erp := mc.FindMonitor("erp")
	if erp == nil || !erp.Disabled || erp.Area != "bj" || erp.Policy != "default" || strings.Join(erp.Nodes, ",") != "bj" {
		t.Fatalf("imported monitor: %+v", erp)
	}
	if !strings.Contains(string(master.app.Config().Text), "imported from bj") {
		t.Fatal("imported records should say where they came from")
	}
	if len(joiner.app.Config().Config.Monitors) != 0 {
		t.Fatal("the joined server must drop its own monitors")
	}

	var st app.AgentState
	waitUntil(t, func() bool {
		joiner.do("GET", "/api/v1/cluster/agent", nil, &st)
		return st.Member != nil && st.Status != nil && st.Status.Connected && st.Status.Node == "bj"
	})
	waitUntil(t, func() bool {
		for _, n := range master.nodeViewsForTest() {
			if n.Name == "bj" && n.Online {
				return true
			}
		}
		return false
	})

	// A code works once; a joined server cannot join again before leaving.
	if c := joiner.do("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": "bj2"}, nil); c != 409 {
		t.Fatalf("second join while joined: %d", c)
	}
	if joiner.do("DELETE", "/api/v1/cluster/agent", nil, nil) != 200 {
		t.Fatal("leave")
	}
	if c := joiner.do("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": "bj2"}, nil); c != 400 {
		t.Fatalf("reused code: %d", c)
	}
	// A master cannot join someone else.
	var other struct {
		Code string `json:"code"`
	}
	joiner.do("POST", "/api/v1/cluster/join-codes", map[string]any{}, &other)
	if c := master.do("POST", "/api/v1/cluster/join", map[string]any{"code": other.Code, "node": "m2"}, nil); c != 409 {
		t.Fatalf("master joining another cluster: %d", c)
	}
}

func (e *env) nodeViewsForTest() []nodeView { return e.srv.nodeViews() }

// A node using its own token is not held to the login limiter, which the
// people signing in from the same address share: only failed tokens count.
func TestAgentRequestsCountOnlyFailedTokens(t *testing.T) {
	master := tlsEnv(t, testBind)
	var added struct {
		Token string `json:"token"`
	}
	if c := master.do("POST", "/api/v1/nodes", map[string]any{"name": "bj"}, &added); c != 200 || added.Token == "" {
		t.Fatalf("node: %d", c)
	}
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second}
	get := func(token string) int {
		req, _ := http.NewRequest("GET", strings.Replace(master.ts.URL, "http://", "https://", 1)+cluster.StatePath, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := range 10 {
		// bj is no standby, so it is refused, but never for its rate.
		if c := get(added.Token); c == http.StatusTooManyRequests {
			t.Fatalf("request %d with a valid token rate-limited", i+1)
		}
	}
	limited := false
	for range 10 {
		limited = limited || get("uka_wrong") == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("guessing tokens must be rate-limited")
	}
}
