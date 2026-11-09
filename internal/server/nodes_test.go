package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
)

func TestNodesAPIAndAgent(t *testing.T) {
	e := newEnv(t, testBind, nil)
	// Serve the same handler on a listener that also speaks TLS, as the
	// real server does, so an agent can connect through the routes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(e.srv.Handler())
	ts.Listener = cluster.SniffListener(ln, e.app.PKI().TLSConfig(nil), time.Second)
	ts.Start()
	defer ts.Close()
	e.ts.Close()
	e.ts = ts
	e.setup()

	var list struct {
		Layout      string     `json:"layout"`
		Nodes       []nodeView `json:"nodes"`
		Fingerprint string     `json:"fingerprint"`
		Server      string     `json:"server"`
	}
	if e.do("GET", "/api/v1/nodes", nil, &list) != 200 || list.Layout != "single" || len(list.Nodes) != 1 || !list.Nodes[0].Self {
		t.Fatalf("single: %+v", list)
	}
	if list.Fingerprint != e.app.PKI().Fingerprint() || !strings.HasPrefix(list.Server, "https://127.0.0.1:") {
		t.Fatalf("fingerprint/server: %+v", list)
	}

	var added struct {
		Node    string          `json:"node"`
		Token   string          `json:"token"`
		Install cluster.Install `json:"install"`
		Serial  uint32          `json:"serial"`
	}
	if c := e.do("POST", "/api/v1/nodes", map[string]any{"name": "bj", "title": "北京机房", "region": "north"}, &added); c != 200 {
		t.Fatalf("add: %d", c)
	}
	if !strings.HasPrefix(added.Token, "uka_") || !strings.Contains(added.Install.Shell, added.Token) ||
		!strings.Contains(added.Install.Systemd, "--ca-fingerprint "+list.Fingerprint) || strings.Contains(added.Install.Shell, "--token ") {
		t.Fatalf("add result: %+v", added)
	}
	if n := e.app.Config().Config.FindNode("bj"); n == nil || n.Description != "北京机房" || n.Region != "north" {
		t.Fatalf("node record: %+v", n)
	}
	for _, name := range []string{"web", "unagikuma", "bad name"} {
		if c := e.do("POST", "/api/v1/nodes", map[string]any{"name": name}, nil); c != 409 && c != 400 {
			t.Fatalf("add %q: %d", name, c)
		}
	}

	ag, err := cluster.NewAgent(cluster.AgentOptions{
		Server: list.Server, Node: "bj", Token: added.Token, Fingerprint: list.Fingerprint, Version: "t", Probe: okProbe,
		MaxBackoff: 200 * time.Millisecond,
		Engine: func(d *engine.Deps) {
			d.Gateways = func() ([]netscope.Gateway, error) { return nil, nil }
			d.HostClock = func() (hostclock.Reading, bool) { return hostclock.Reading{}, false }
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	agentDone := make(chan struct{})
	go func() { ag.Run(ctx); close(agentDone) }()
	defer func() { cancel(); <-agentDone }()

	bj := func() nodeView {
		var l struct {
			Nodes []nodeView `json:"nodes"`
		}
		e.do("GET", "/api/v1/nodes", nil, &l)
		for _, n := range l.Nodes {
			if n.Name == "bj" {
				return n
			}
		}
		return nodeView{}
	}
	waitUntil(t, func() bool {
		n := bj()
		return n.Online && n.Synced && n.ClockOK && n.Token != nil && n.Token.LastUsed != nil
	})
	var ov struct {
		Layout string     `json:"layout"`
		Nodes  []nodeView `json:"nodes"`
	}
	if e.do("GET", "/api/v1/overview", nil, &ov) != 200 || ov.Layout != "cluster" || len(ov.Nodes) != 2 {
		t.Fatalf("overview: %+v", ov)
	}
	waitUntil(t, func() bool {
		r, _ := e.app.Engine().Row("web")
		return !r.LastCheck.IsZero()
	})

	var det struct {
		Row rowJSON `json:"row"`
	}
	if e.do("GET", "/api/v1/monitors/web", nil, &det) != 200 || len(det.Row.Votes) != 0 || det.Row.Quorum != nil {
		t.Fatalf("row votes: %+v %+v", det.Row.Votes, det.Row.Quorum)
	}
	var push struct {
		Row rowJSON `json:"row"`
	}
	if e.do("GET", "/api/v1/monitors/backup", nil, &push) != 200 || push.Row.Votes != nil || push.Row.Quorum != nil {
		t.Fatalf("push monitor has no votes: %+v", push.Row)
	}

	// A new token ends the session made with the old one.
	if e.do("POST", "/api/v1/nodes/bj/token", nil, nil) != 200 {
		t.Fatal("rotate")
	}
	waitUntil(t, func() bool { return !bj().Online })

	if e.do("DELETE", "/api/v1/nodes/bj", nil, nil) != 200 || e.app.Config().Config.FindNode("bj") != nil {
		t.Fatal("delete")
	}
	if toks, _ := e.app.Store().AgentTokens(); len(toks) != 0 {
		t.Fatalf("token kept after delete: %+v", toks)
	}
	if e.do("POST", "/api/v1/nodes/bj/token", nil, nil) != http.StatusNotFound {
		t.Fatal("token for a deleted node")
	}
}

func TestNodeUpdate(t *testing.T) {
	e := newEnv(t, testBind+"bj IN NODE \"old\" region=north local-detect=no\n", nil)
	e.setup()
	f := false
	if c := e.do("PUT", "/api/v1/nodes/bj", map[string]any{"title": "北京机房", "region": "east", "probe": f, "anchors": "lan:10.0.0.1;wan:223.5.5.5"}, nil); c != 200 {
		t.Fatalf("update: %d", c)
	}
	n := e.app.Config().Config.FindNode("bj")
	if n.Description != "北京机房" || n.Region != "east" || n.Probe || n.LocalDetect || len(n.AnchorsWAN) != 1 {
		t.Fatalf("node: %+v", n)
	}
	// The master's own record can be created from the form.
	if c := e.do("PUT", "/api/v1/nodes/unagikuma", map[string]any{"title": "调度", "probe": f}, nil); c != 200 {
		t.Fatalf("self: %d", c)
	}
	if m := e.app.Config().Config.FindNode("unagikuma"); m == nil || m.Probe {
		t.Fatalf("self record: %+v", m)
	}
	if c := e.do("PUT", "/api/v1/nodes/bj", map[string]any{"policy": "nope"}, nil); c != 400 {
		t.Fatalf("bad policy: %d", c)
	}
	if c := e.do("PUT", "/api/v1/nodes/none", map[string]any{}, nil); c != 404 {
		t.Fatalf("unknown node: %d", c)
	}
}
