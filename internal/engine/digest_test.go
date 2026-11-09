package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// A storm of notices becomes summaries that say what kind of problem it
// is: services for their owners, a failing segment or DNS for the network
// team (to its own channel when the policy names one).
func TestDigestTellsNetworkFromServices(t *testing.T) {
	for _, withNetwork := range []bool{false, true} {
		var sent []*Intent
		sink := &recordSink{memSink: newMemSink(), saved: &sent}
		e := New(Deps{Sink: sink})
		ts, _ := tunables.New(map[string]string{"notify.coalesce-queue": "3"})
		pol := PolicySpec{Name: "default", Chain: []string{"ops"}, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts)}
		if withNetwork {
			pol.Network = []string{"netadmin"}
		}
		log := func(n string) ChannelSpec { return ChannelSpec{Channel: notify.Channel{Name: n, Kind: "log"}} }
		e.cfg = Config{Tunables: ts, Location: time.UTC, Policies: map[string]PolicySpec{"default": pol},
			Channels: map[string]ChannelSpec{"ops": log("ops"), "netadmin": log("netadmin")}}
		mon := func(name, reason, domain string) {
			e.states[name] = &monRuntime{LastReason: reason, Domain: domain}
			e.enqueue(&Intent{IncidentID: int64(len(e.states)), Kind: "PROBLEM", Policy: "default", CreatedAt: t0, DueAt: t0,
				Message: notify.Message{Kind: "PROBLEM", Monitor: name, Title: name}})
		}
		for _, n := range []string{"db1", "db2", "db3", "db4"} {
			mon(n, probe.ReasonTimeout, "4:10.0.3.0/24") // one segment, network-level
		}
		for _, n := range []string{"a", "b", "c"} {
			mon(n, probe.ReasonDNSResolve, "n:"+n+".example") // names do not resolve
		}
		mon("shop", probe.ReasonHTTPStatus, "4:203.0.113.0/24") // a service answering wrong
		mon("api", probe.ReasonTimeout, "4:198.51.100.0/24")    // alone in its segment: a service
		sent = nil
		e.coalesce(t0.Add(time.Minute))

		var digests []*Intent
		for _, in := range sent {
			if in.Kind == "DIGEST" && in.State == IntentPending {
				digests = append(digests, in)
			}
		}
		want := 1
		if withNetwork {
			want = 2
		}
		if len(digests) != want {
			t.Fatalf("network=%v: %d digests", withNetwork, len(digests))
		}
		svc := digests[0]
		if !strings.Contains(svc.Message.Title, "Services failing: 2") || !strings.Contains(svc.Message.Body, "shop") || !strings.Contains(svc.Message.Body, "api") {
			t.Errorf("network=%v: service digest %q / %q", withNetwork, svc.Message.Title, svc.Message.Body)
		}
		net := svc
		if withNetwork {
			net = digests[1]
			if len(net.Chain) != 1 || net.Chain[0] != "netadmin" || strings.Contains(svc.Message.Body, "db1") {
				t.Errorf("the network summary goes to netadmin only: %v / %q", net.Chain, svc.Message.Body)
			}
		}
		if !strings.Contains(net.Message.Body, "10.0.3.0/24") || !strings.Contains(net.Message.Body, "DNS") || !strings.Contains(net.Message.Body, "db4") {
			t.Errorf("network=%v: network causes %q", withNetwork, net.Message.Body)
		}
		if strings.Index(svc.Message.Body, "shop") > strings.Index(svc.Message.Body, "10.0.3.0/24") && !withNetwork {
			t.Errorf("services must come first: %q", svc.Message.Body)
		}
	}
}

// recordSink keeps every saved intent.
type recordSink struct {
	*memSink
	saved *[]*Intent
}

func (s *recordSink) SaveIntent(in *Intent) error {
	c := *in
	*s.saved = append(*s.saved, &c)
	return nil
}
