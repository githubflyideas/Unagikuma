package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/hostclock"
	"github.com/githubflyideas/unagikuma/internal/netscope"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/schedule"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// A monitor goes down: its policy notifies a contact group whose members
// are limited by time periods, escalates to another channel while the
// incident stays unacknowledged, and the event handler runs.
func TestPeriodGroupEscalationHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the handler is a shell script")
	}
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	script := "#!/bin/sh\necho \"$1 $UNAGIKUMA_STATE_TYPE\" >> " + mark + "\necho handled\n"
	if err := os.WriteFile(filepath.Join(dir, "fix.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	probe.SetPluginDirs([]string{dir})
	t.Cleanup(func() { probe.SetPluginDirs(nil) })

	var mu sync.Mutex
	var sent []string
	sink := newMemSink()
	e := New(Deps{
		Probe: func(context.Context, probe.Spec) probe.Result {
			return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Evidence: &probe.Evidence{}}
		},
		Send: func(_ context.Context, ch notify.Channel, m *notify.Message) notify.Attempt {
			mu.Lock()
			sent = append(sent, ch.Name+":"+m.Kind)
			mu.Unlock()
			return notify.Attempt{OK: true}
		},
		Sink: sink, HostClock: func() (hostclock.Reading, bool) { return hostclock.Reading{}, false },
		Gateways: func() ([]netscope.Gateway, error) { return nil, nil },
	})
	ts, err := tunables.New(map[string]string{
		"engine.first-round-delay": "0s", "suspend.recheck-spread": "0s", "suspend.tick": "200ms",
		"engine.round-grace": "0s", "localnet.enabled": "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	always, _ := schedule.ParsePeriod("mon-sun 00:00-24:00")
	log := func(name string) ChannelSpec { return ChannelSpec{Channel: notify.Channel{Name: name, Kind: "log"}} }
	im, sms := log("im"), log("sms")
	im.Period, im.Outside = &always, true // never usable
	ops := log("ops")
	ops.Kind, ops.Members = "group", []string{"im", "sms"}
	e.Apply(Config{Tunables: ts, Location: time.UTC,
		Channels: map[string]ChannelSpec{"im": im, "sms": sms, "ops": ops, "boss": log("boss")},
		Policies: map[string]PolicySpec{"pager": {Name: "pager", Chain: []string{"ops"}, Mode: DeliverFallback, Notify: DefaultNotifyPolicy(ts),
			Escalate: []EscalationStep{{After: 400 * time.Millisecond, Channels: []string{"boss"}}}}},
		Monitors: []MonitorSpec{{Name: "web", Title: "web", Policy: "pager", Handler: "fix.sh $SERVICESTATE$",
			Probe:    probe.Spec{Name: "web", Type: "TCP", Target: "10.0.0.1:80", Timeout: 100 * time.Millisecond},
			Interval: 50 * time.Millisecond, Attempts: 1, RecoverAttempts: 1, DefHash: "web"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(sent, " ")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(got(), "boss:ESCALATE") {
		if time.Now().After(deadline) {
			t.Fatalf("no escalation: %q", got())
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := got()
	if !strings.Contains(s, "sms:PROBLEM") || strings.Contains(s, "im:") || strings.Count(s, "ESCALATE") != 1 {
		t.Fatalf("sent %q: the group must reach sms only, and escalate once", s)
	}
	for {
		b, _ := os.ReadFile(mark)
		if strings.Contains(string(b), "CRITICAL HARD") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handler did not run: %q", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var esc, ran bool
	for deadline = time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !(esc && ran); time.Sleep(20 * time.Millisecond) {
		sink.mu.Lock()
		for _, ev := range sink.events {
			esc = esc || ev.Data["msg"] == "escalated"
			ran = ran || (ev.Data["msg"] == "handler_ran" && ev.Data["output"] == "handled")
		}
		sink.mu.Unlock()
	}
	if !esc || !ran {
		t.Fatalf("events: escalated=%v handler_ran=%v", esc, ran)
	}
}
