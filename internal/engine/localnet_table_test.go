package engine

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
)

// Scenarios of the local network detector: what it concludes, and which
// monitors it then holds back.
func TestLocalNetScenarios(t *testing.T) {
	type step func(d *LocalNet)
	at := func(s int) time.Time { return t0.Add(sec(s)) }
	fails := func(s, n int) step {
		return func(d *LocalNet) {
			for i := range n {
				d.Observe(wanFail(at(s), fmt.Sprintf("d%d.example", i)))
			}
		}
	}
	ok := func(s int, dom string) step { return func(d *LocalNet) { d.Observe(wanOK(at(s), dom)) } }
	anchor := func(s int, kind AnchorKind, addr string, okk bool, times int) step {
		return func(d *LocalNet) {
			for i := range times {
				d.Anchor(AnchorResult{Kind: kind, Family: FamilyV4, Addr: addr, OK: okk, At: at(s + i)})
			}
		}
	}
	unresolved := func(s, n int) step {
		return func(d *LocalNet) {
			for i := range n {
				d.Observe(Observation{At: at(s), Scope: netscope.WAN, Domain: fmt.Sprintf("n%d.example", i), ByName: true, DNSFail: true, NetworkFail: true})
			}
		}
	}
	dnsFails := func(s, n int) step {
		return func(d *LocalNet) {
			for i := range n {
				d.Observe(Observation{At: at(s), Scope: netscope.WAN, Domain: fmt.Sprintf("q%d.example", i), ByName: true, DNSFail: true})
			}
		}
	}
	eval := func(s int) step { return func(d *LocalNet) { d.Evaluate(at(s)) } }

	cases := []struct {
		name    string
		steps   []step
		active  string // anomaly kinds, sorted, comma separated
		wanHeld bool   // a wan monitor is held at the end
		pending bool   // an outage is being assessed at the end
		end     int
	}{
		{"healthy network", []step{ok(1, "a.example"), ok(1, "b.example"), eval(2)}, "", false, false, 2},
		{"three domains fail, nothing answers", []step{fails(1, 3), eval(2)}, "uplink_down", true, false, 2},
		{"two domains are not enough", []step{fails(1, 2), eval(2)}, "", false, true, 2},
		{"one reply vetoes the episode", []step{fails(1, 3), ok(1, "x.example"), eval(2)}, "", false, false, 2},
		{"targets dead before do not count", []step{fails(1, 2), ok(50, "x.example"), fails(100, 3), eval(101)}, "", false, false, 101},
		{"wan anchors all down", []step{anchor(1, AnchorWAN, "223.5.5.5", false, 3), eval(5)}, "uplink_down", true, false, 5},
		{"a wan anchor answers", []step{anchor(1, AnchorWAN, "223.5.5.5", true, 1), fails(2, 5), eval(3)}, "", false, false, 3},
		{"router down", []step{anchor(1, AnchorLAN, "192.168.1.1", false, 3), eval(5)}, "gateway_down", true, false, 5},
		{"router silent but traffic flows", []step{ok(1, "a.example"), anchor(1, AnchorLAN, "192.168.1.1", false, 3), eval(5)}, "", false, false, 5},
		{"resolver broken, network fine", []step{ok(1, "a.example"), dnsFails(1, 2), eval(2)}, "dns_down", false, false, 2},
		{"cold start without network", []step{unresolved(1, 3), eval(2)}, "uplink_down", true, false, 2},
		{"cold start, first failures", []step{unresolved(1, 1)}, "", false, true, 1},
		{"outage over after recovery", []step{fails(1, 3), eval(2), ok(10, "d0.example"), ok(10, "d1.example"), eval(11), ok(80, "d0.example"), ok(80, "d1.example"), eval(81)}, "", false, false, 81},
		{"undecided for too long", []step{fails(1, 1)}, "", false, false, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := defaultLocalNet(t)
			for _, s := range c.steps {
				s(d)
			}
			var kinds []string
			for _, a := range d.Active() {
				kinds = append(kinds, a.Kind)
			}
			sort.Strings(kinds)
			if got := strings.Join(kinds, ","); got != c.active {
				t.Errorf("active %q, want %q", got, c.active)
			}
			if held, _ := d.Affects(netscope.WAN, FamilyV4, false, false); held != c.wanHeld {
				t.Errorf("wan monitor held %v, want %v", held, c.wanHeld)
			}
			if p := d.Pending(netscope.WAN, 0, at(c.end)); p != c.pending {
				t.Errorf("pending %v, want %v", p, c.pending)
			}
		})
	}
}
