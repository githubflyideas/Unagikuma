package engine

import (
	"strings"
	"testing"
	"time"
)

// The abnormal-exit notice must not invent a downtime: with no persisted
// heartbeat the duration is unknown, so the message states no duration
// and the event carries no down_s. With a heartbeat, the computed
// duration is reported as before.
func TestStartupNoticeAbnormalExitDuration(t *testing.T) {
	newEngine := func() (*Engine, *memSink) {
		sink := newMemSink()
		e := New(Deps{Sink: sink})
		e.Apply(Config{Lang: "en", Location: time.UTC})
		return e, sink
	}
	find := func(sink *memSink) Event {
		t.Helper()
		for _, ev := range sink.events {
			if ev.Data["msg"] == "abnormal_exit" {
				return ev
			}
		}
		t.Fatalf("no abnormal_exit event in %+v", sink.events)
		return Event{}
	}

	e, sink := newEngine()
	e.StartupNotice(false, time.Time{})
	ev := find(sink)
	if _, ok := ev.Data["down_s"]; ok {
		t.Fatalf("unknown downtime must not carry down_s: %+v", ev.Data)
	}
	if strings.Contains(ev.Message, "0s") {
		t.Fatalf("unknown downtime stated as a duration: %q", ev.Message)
	}
	if !strings.Contains(ev.Message, "unknown time") {
		t.Fatalf("message should say the downtime is unknown: %q", ev.Message)
	}

	e, sink = newEngine()
	e.StartupNotice(false, time.Now().Add(-2*time.Minute))
	ev = find(sink)
	ds, ok := ev.Data["down_s"].(int)
	if !ok || ds < 110 || ds > 130 {
		t.Fatalf("known downtime: down_s = %v", ev.Data["down_s"])
	}
	if !strings.Contains(ev.Message, "down for") {
		t.Fatalf("known downtime should be stated: %q", ev.Message)
	}
}
