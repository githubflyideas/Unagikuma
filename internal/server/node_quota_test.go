package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/license"
)

type nodeQuotaErr struct {
	Code        string
	Message     string
	Diagnostics []struct{ Code string } `json:"diagnostics"`
}

func (e *env) addNode(name string, want int) nodeQuotaErr {
	e.t.Helper()
	var er nodeQuotaErr
	if c := e.do("POST", "/api/v1/nodes", map[string]any{"name": name}, &er); c != want {
		e.t.Fatalf("add node %s: %d %+v, want %d", name, c, er, want)
	}
	return er
}

func (e *env) nodeCount() int {
	e.t.Helper()
	var list struct {
		Nodes []nodeView `json:"nodes"`
	}
	if c := e.do("GET", "/api/v1/nodes", nil, &list); c != 200 {
		e.t.Fatalf("list nodes: %d", c)
	}
	return len(list.Nodes)
}

func (e *env) installLicense(f license.File) {
	e.t.Helper()
	f.Customer, f.Licensee, f.Product, f.License = "Co", license.LicenseeOrganization, license.Product, license.TypePerpetual
	b, _ := json.Marshal(license.Sign(testLicenseKey, f))
	if _, err := e.app.License().Install(b, time.Now()); err != nil {
		e.t.Fatal(err)
	}
	e.app.LicenseChanged()
}

// Without a licence a cluster has three nodes, the master counted: the
// fourth is refused as 402 quota_exceeded with a message that says so,
// and the nodes that are there stay.
func TestNodeQuotaUnlicensed(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	e.addNode("n1", 200)
	e.addNode("n2", 200)
	if n := e.nodeCount(); n != 3 {
		t.Fatalf("master and two nodes: %d", n)
	}
	er := e.addNode("n3", 402)
	if er.Code != CodeQuota || !strings.Contains(er.Message, "3 nodes") || !strings.Contains(er.Message, "master") ||
		len(er.Diagnostics) != 0 {
		t.Fatalf("fourth node refusal: %+v", er)
	}
	if n := e.nodeCount(); n != 3 {
		t.Fatalf("nodes after the refusal: %d", n)
	}
	// The licence page and the overview say where things stand.
	var lic struct {
		Registered bool `json:"registered"`
		NodeLimit  int  `json:"node_limit"`
		FreeNodes  int  `json:"free_nodes"`
		MaxNodes   int  `json:"max_nodes"`
		Usage      struct {
			Nodes int `json:"nodes"`
		} `json:"usage"`
	}
	if c := e.do("GET", "/api/v1/license", nil, &lic); c != 200 || lic.Registered || lic.NodeLimit != 3 || lic.FreeNodes != 3 || lic.Usage.Nodes != 3 {
		t.Fatalf("licence surface: %d %+v", c, lic)
	}
	var ov map[string]any
	e.do("GET", "/api/v1/overview", nil, &ov)
	if _, over := ov["node_over"]; over {
		t.Fatalf("three nodes are not over the limit: %v", ov["node_over"])
	}
}

// A licence with max_nodes lifts the limit to its own number, one that
// names less gives the free three, and one without the field no limit.
func TestNodeQuotaLicensed(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	e.addNode("n1", 200)
	e.addNode("n2", 200)
	e.addNode("n3", 402)

	e.installLicense(license.File{MaxNodes: 2})
	e.addNode("n3", 402)

	e.installLicense(license.File{MaxNodes: 5})
	var lic struct {
		NodeLimit int `json:"node_limit"`
		MaxNodes  int `json:"max_nodes"`
	}
	if c := e.do("GET", "/api/v1/license", nil, &lic); c != 200 || lic.NodeLimit != 5 || lic.MaxNodes != 5 {
		t.Fatalf("licence surface: %d %+v", c, lic)
	}
	e.addNode("n3", 200)
	e.addNode("n4", 200)
	if er := e.addNode("n5", 402); er.Code != CodeQuota || !strings.Contains(er.Message, "5 nodes") {
		t.Fatalf("sixth machine: %+v", er)
	}

	e.installLicense(license.File{})
	if c := e.do("GET", "/api/v1/license", nil, &lic); c != 200 || lic.NodeLimit != 0 {
		t.Fatalf("licence without max_nodes: %d %+v", c, lic)
	}
	e.addNode("n5", 200)
	e.addNode("n6", 200)
}

// An expired licence is the free limit again: nothing is dropped, nothing
// is added, and the console says it is over.
func TestNodeQuotaExpired(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	exp := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	e.installLicense(license.File{MaxNodes: 6, Expires: &exp, License: license.TypeSubscription})
	for _, n := range []string{"n1", "n2", "n3", "n4"} {
		e.addNode(n, 200)
	}
	if st := e.app.License().Check(time.Now().AddDate(0, 0, 30)); st.Registered {
		t.Fatalf("licence still valid: %+v", st)
	}
	er := e.addNode("n5", 402)
	if er.Code != CodeQuota || !strings.Contains(er.Message, "3 nodes") {
		t.Fatalf("node after expiry: %+v", er)
	}
	if n := e.nodeCount(); n != 5 {
		t.Fatalf("nodes after expiry: %d, want the 5 there", n)
	}
	var ov struct {
		NodeOver *struct {
			Used       int  `json:"used"`
			Limit      int  `json:"limit"`
			Over       bool `json:"over"`
			Registered bool `json:"registered"`
		} `json:"node_over"`
	}
	if c := e.do("GET", "/api/v1/overview", nil, &ov); c != 200 || ov.NodeOver == nil || ov.NodeOver.Used != 5 || ov.NodeOver.Limit != 3 || !ov.NodeOver.Over || ov.NodeOver.Registered {
		t.Fatalf("overview after expiry: %d %+v", c, ov.NodeOver)
	}
	// A node can still be removed, and the room it leaves is usable.
	if c := e.do("DELETE", "/api/v1/nodes/n4", nil, nil); c != 204 {
		t.Fatalf("remove a node over the limit: %d", c)
	}
	e.addNode("n5", 402) // four nodes: still over the three
}

// A machine joining with a code is held to the limit like one added by
// hand: the fourth is refused with 402 quota_exceeded.
func TestNodeQuotaJoin(t *testing.T) {
	master := tlsEnv(t, testBind+fastLogin)
	master.addNode("n1", 200)
	master.addNode("n2", 200)
	var jc struct {
		Code string `json:"code"`
	}
	if master.do("POST", "/api/v1/cluster/join_codes", map[string]any{}, &jc) != 200 {
		t.Fatal("join code")
	}
	joiner := tlsEnv(t, `@ IN SOA box. admin.localhost. ( 2026102801 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s
web IN TCP 10.9.9.6:443
`)
	var er nodeQuotaErr
	c := joiner.do("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": "bj"}, &er)
	if c != 402 || er.Code != CodeQuota || !strings.Contains(er.Message, "3 nodes") {
		t.Fatalf("fourth machine joining: %d %+v", c, er)
	}
	if master.app.Config().Config.FindNode("bj") != nil {
		t.Fatal("the refused machine is a node")
	}
	// The code was not spent: with room (or a licence) the same code joins.
	master.installLicense(license.File{MaxNodes: 4})
	if c := joiner.do("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": "bj"}, nil); c != 200 {
		t.Fatalf("join under a licence: %d", c)
	}
}

// The session no longer carries a site view trial: it is always on.
func TestSessionHasNoSiteViewTrial(t *testing.T) {
	e := newEnv(t, usersBind, nil)
	e.setup()
	var me map[string]any
	if c := e.do("GET", "/api/v1/session", nil, &me); c != 200 || me["cluster"] != true {
		t.Fatalf("session: %d %v", c, me)
	}
	if _, ok := me["site_view"]; ok {
		t.Fatalf("the session still carries a site view trial: %v", me["site_view"])
	}
}

// Whole-file saves meet the same ceiling as the node API: a text with a
// fourth machine in it is refused as 402 quota_exceeded with the
// node_limit diagnostic, and the file on disk is untouched.
func TestNodeQuotaConfigFile(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	e.addNode("n1", 200)
	e.addNode("n2", 200)
	var cfg struct {
		Text string `json:"text"`
		ETag string `json:"etag"`
	}
	if c := e.do("GET", "/api/v1/config", nil, &cfg); c != 200 {
		t.Fatalf("config: %d", c)
	}
	text := cfg.Text + "\nn3 IN NODE \"site 3\"\n"
	var er nodeQuotaErr
	if c := e.do("PUT", "/api/v1/config", map[string]string{"text": text}, &er, "If-Match", cfg.ETag); c != 402 {
		t.Fatalf("a text with a fourth machine: %d %+v", c, er)
	}
	if er.Code != CodeQuota || len(er.Diagnostics) != 1 || er.Diagnostics[0].Code != "node_limit" {
		t.Fatalf("refusal: %+v", er)
	}
	if e.app.Config().Config.FindNode("n3") != nil {
		t.Fatal("the refused node is in the configuration")
	}
	e.installLicense(license.File{MaxNodes: 4})
	if c := e.do("PUT", "/api/v1/config", map[string]string{"text": text}, &er, "If-Match", cfg.ETag); c != 200 {
		t.Fatalf("the same text under a licence: %d %+v", c, er)
	}
}
