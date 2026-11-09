package engine

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/netscope"
)

// A local outage that outlasts localnet.max-hold stops holding the
// monitors back (fail open): a wrong "it is our network" judgement must
// never hide a real incident for good.
func TestMaxHoldFailsOpen(t *testing.T) {
	d, _ := defaultLocalNet(t)
	if d.cfg.MaxHold != 15*time.Minute {
		t.Fatalf("default max hold: %v", d.cfg.MaxHold)
	}
	doms := []string{"4:1.1.1.0/24", "4:8.8.8.0/24", "4:9.9.9.0/24"}
	for _, dm := range doms {
		d.Observe(wanOK(t0, dm))
	}
	d.Evaluate(t0)
	for i, dm := range doms {
		d.Observe(wanFail(t0.Add(sec(10+i)), dm))
	}
	if !enters(d.Evaluate(t0.Add(sec(13))), AnomalyUplink) {
		t.Fatal("outage not recognised")
	}
	if _, ok := d.Release(t0.Add(14 * time.Minute)); ok {
		t.Fatal("released before max hold")
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV4, false, false); !ex {
		t.Fatal("wan must be held during the outage")
	}
	since, ok := d.Release(t0.Add(16 * time.Minute))
	if !ok || !since.Equal(t0.Add(sec(10))) {
		t.Fatalf("release after max hold: %v %v", since, ok)
	}
	if _, ok := d.Release(t0.Add(17 * time.Minute)); ok {
		t.Fatal("released twice")
	}
	if ex, _ := d.Affects(netscope.WAN, FamilyV4, false, false); ex {
		t.Fatal("released: votes must count again")
	}
	if d.Pending(netscope.WAN, FamilyV4, t0.Add(17*time.Minute)) {
		t.Fatal("released: nothing pending")
	}
	// The outage ends; the next one is held again.
	now := t0.Add(18 * time.Minute)
	for _, dm := range doms {
		d.Observe(wanOK(now, dm))
	}
	for i := 0; i < 5 && len(d.Active()) > 0; i++ {
		now = now.Add(time.Minute)
		for _, dm := range doms {
			d.Observe(wanOK(now, dm))
		}
		d.Evaluate(now)
	}
	if len(d.Active()) > 0 || d.Released() {
		t.Fatalf("outage should have ended and the release reset: %+v %v", d.Active(), d.Released())
	}
}

func TestMaxHoldOff(t *testing.T) {
	d, _ := defaultLocalNet(t)
	d.cfg.MaxHold = 0
	doms := []string{"4:1.1.1.0/24", "4:8.8.8.0/24", "4:9.9.9.0/24"}
	for _, dm := range doms {
		d.Observe(wanOK(t0, dm))
	}
	d.Evaluate(t0)
	for i, dm := range doms {
		d.Observe(wanFail(t0.Add(sec(10+i)), dm))
	}
	d.Evaluate(t0.Add(sec(13)))
	if _, ok := d.Release(t0.Add(48 * time.Hour)); ok {
		t.Fatal("max-hold 0 never releases")
	}
}
