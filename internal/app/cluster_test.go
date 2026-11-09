package app

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

func TestAgentEndToEnd(t *testing.T) {
	a, fp, _, _ := newApp(t, testConfig+`bj IN NODE "北京"
`)
	tok, err := a.Store().CreateAgentToken("bj")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET "+cluster.ConnectPath, a.Cluster())
	hs := &http.Server{Handler: mux}
	go hs.Serve(cluster.SniffListener(raw, a.PKI().TLSConfig(nil), time.Second))
	defer hs.Close()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	defer func() { cancel(); <-runDone }()

	var agentFails atomic.Bool
	var targetChecks atomic.Int32
	ag, err := cluster.NewAgent(cluster.AgentOptions{
		Server: "https://" + raw.Addr().String(), Node: "bj", Token: tok, Fingerprint: a.PKI().Fingerprint(), Version: "e2e",
		MaxBackoff: 200 * time.Millisecond,
		Probe: func(_ context.Context, s probe.Spec) probe.Result {
			if s.Name == "web" {
				targetChecks.Add(1)
			}
			if agentFails.Load() {
				return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Evidence: &probe.Evidence{}}
			}
			return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Latency: time.Millisecond, Evidence: &probe.Evidence{}}
		},
		Engine: func(d *engine.Deps) {
			d.Gateways = func() ([]netscope.Gateway, error) { return nil, nil }
			d.HostClock = func() (hostclock.Reading, bool) { return hostclock.Reading{}, false }
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agentDone := make(chan struct{})
	go func() { ag.Run(ctx); close(agentDone) }()
	defer func() { cancel(); <-agentDone }()

	waitFor(t, "control connection and local monitoring", 10*time.Second, func() bool {
		r, _ := a.Engine().Row("web")
		nodes := a.Engine().Nodes()
		return r.Status == "ok" && len(nodes) == 1 && nodes[0].Online
	})
	nodes := a.Engine().Nodes()
	if len(nodes) != 1 || !nodes[0].Online || nodes[0].Serial != a.Config().Serial || nodes[0].Version != "e2e" {
		t.Fatalf("nodes: %+v", nodes)
	}

	// A remote agent's opinion cannot gate the local problem or recovery.
	agentFails.Store(true)
	fp.set("web", true)
	waitFor(t, "local problem", 10*time.Second, func() bool { r, _ := a.Engine().Row("web"); return r.Status == "critical" && r.IncidentID != 0 })
	fp.set("web", false)
	waitFor(t, "local recovery", 10*time.Second, func() bool { r, _ := a.Engine().Row("web"); return r.Status == "ok" })
	if targetChecks.Load() != 0 {
		t.Fatal("legacy remote scheduling remains")
	}
}
