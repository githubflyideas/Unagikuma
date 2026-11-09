package engine

import (
	"context"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// The overview counter and the problems list use the same definition:
// whatever mix of incidents and UNKNOWN monitors there is, the count is
// the length of the unhandled list.
func TestOneUnhandledDefinition(t *testing.T) {
	now := time.Now()
	open := func(id int64, kind string, f func(*IncidentState)) *Incident {
		inc := &Incident{ID: id, Monitor: "m", Kind: kind}
		inc.State.OpenedAt = now
		if f != nil {
			f(&inc.State)
		}
		return inc
	}
	for _, c := range []struct {
		name      string
		incidents []*Incident
		want      int
	}{
		{"incidents only", []*Incident{open(1, "", nil), open(2, "", func(s *IncidentState) { s.Acked = true })}, 1},
		{"with an UNKNOWN monitor", []*Incident{open(1, "", nil)}, 1},
		{"low and regional", []*Incident{open(1, "", nil), open(2, IncidentLow, nil), open(3, IncidentRegional, nil),
			open(4, "", func(s *IncidentState) { s.SilencedUntil = now.Add(time.Hour) }),
			open(5, "", func(s *IncidentState) { s.Drill = true }),
			open(6, "", func(s *IncidentState) { s.ResolvedAt = now })}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := New(Deps{
				Probe: func(context.Context, probe.Spec) probe.Result {
					return probe.Result{Vote: probe.VoteAbstain, Reason: probe.ReasonConnRefused, Evidence: &probe.Evidence{}}
				},
				Sink: newMemSink(), HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
				Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
			})
			ts, _ := tunables.New(map[string]string{"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "localnet.enabled": "off"})
			e.Apply(Config{Tunables: ts, Location: time.UTC, Monitors: []MonitorSpec{{Name: "u", Title: "u",
				Probe: probe.Spec{Name: "u", Type: "TCP", Target: "10.0.0.1:80", Timeout: 50 * time.Millisecond}, Interval: 50 * time.Millisecond, Attempts: 1, DefHash: "u"}}})
			for _, inc := range c.incidents {
				e.incidents[inc.ID] = inc
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { e.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			if c.name == "with an UNKNOWN monitor" {
				for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
					if r, _ := e.Row("u"); r.Status == StatusUnknown {
						break
					}
					if time.Now().After(deadline) {
						r, _ := e.Row("u")
						t.Fatalf("monitor never UNKNOWN: %s", r.Status)
					}
				}
			}
			list := 0
			for _, inc := range e.OpenIncidents() {
				if Unhandled(&inc, time.Now()) {
					list++
				}
			}
			if got := e.Overview().Unhandled; got != c.want || list != c.want {
				t.Fatalf("overview %d, list %d, want %d", got, list, c.want)
			}
		})
	}
}
