package engine

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestDeliveryFallbackChain(t *testing.T) {
	d := NewDelivery([]string{"feishu", "tg", "mail"}, "")
	if d.Mode != DeliverFallback {
		t.Fatalf("default mode %q", d.Mode)
	}
	idx, st := d.Next(t0, nil)
	if !reflect.DeepEqual(idx, []int{0}) || st != DeliveryActive {
		t.Fatalf("first: %v %v", idx, st)
	}
	d.Begin(0)
	if idx, st := d.Next(t0, nil); idx != nil || st != DeliveryActive {
		t.Fatalf("in flight: %v %v", idx, st)
	}
	d.Finish(0, false, "code=19001", t0, 0, 2*time.Second)
	idx, _ = d.Next(t0, nil)
	if !reflect.DeepEqual(idx, []int{1}) || d.Pos() != 1 {
		t.Fatalf("falls back: %v pos=%d", idx, d.Pos())
	}
	d.Begin(1)
	d.Finish(1, true, "", t0, 0, 0)
	if st := d.Status(); st != DeliveryDone || !d.Fallback() {
		t.Fatalf("done via fallback: %v %v", st, d.Fallback())
	}
	if idx, st := d.Next(t0, nil); idx != nil || st != DeliveryDone {
		t.Fatalf("nothing after success: %v %v", idx, st)
	}
}

func TestDeliveryRetriesWithBackoff(t *testing.T) {
	d := NewDelivery([]string{"feishu", "tg"}, DeliverFallback)
	d.Begin(0)
	d.Finish(0, false, "timeout", t0, 1, 2*time.Second)
	if idx, st := d.Next(t0.Add(time.Second), nil); idx != nil || st != DeliveryWaiting {
		t.Fatalf("backoff: %v %v", idx, st)
	}
	idx, _ := d.Next(t0.Add(2*time.Second), nil)
	if !reflect.DeepEqual(idx, []int{0}) {
		t.Fatalf("retry same channel: %v", idx)
	}
	d.Begin(0)
	d.Finish(0, false, "timeout", t0.Add(2*time.Second), 1, 2*time.Second)
	if idx, _ := d.Next(t0.Add(2*time.Second), nil); !reflect.DeepEqual(idx, []int{1}) {
		t.Fatalf("retries exhausted, next channel: %v", idx)
	}
	d.Begin(1)
	d.Finish(1, false, "500", t0, 0, 0)
	if st := d.Status(); st != DeliveryFailed {
		t.Fatalf("all failed: %v", st)
	}
}

func TestDeliveryPausedChannels(t *testing.T) {
	d := NewDelivery([]string{"feishu", "lan-smtp"}, DeliverFallback)
	wanDown := func(ch string) bool { return ch == "feishu" }
	idx, _ := d.Next(t0, wanDown)
	if !reflect.DeepEqual(idx, []int{1}) {
		t.Fatalf("lan channel used during the outage: %v", idx)
	}
	d.Begin(1)
	d.Finish(1, false, "refused", t0, 0, 0)
	// The paused wan channel is still owed the message.
	if idx, st := d.Next(t0, wanDown); idx != nil || st != DeliveryWaiting {
		t.Fatalf("waits for the paused channel: %v %v", idx, st)
	}
	if idx, _ := d.Next(t0.Add(time.Hour), nil); !reflect.DeepEqual(idx, []int{0}) {
		t.Fatalf("resumes after the outage: %v", idx)
	}
	d.Begin(0)
	d.Finish(0, true, "", t0.Add(time.Hour), 0, 0)
	// Delivered on the preferred channel after all: not a fallback.
	if d.Status() != DeliveryDone || d.Fallback() {
		t.Fatal("done")
	}
}

func TestDeliveryAllMode(t *testing.T) {
	d := NewDelivery([]string{"a", "b", "c"}, DeliverAll)
	idx, _ := d.Next(t0, func(ch string) bool { return ch == "c" })
	if !reflect.DeepEqual(idx, []int{0, 1}) {
		t.Fatalf("all ready steps: %v", idx)
	}
	d.Begin(0)
	d.Begin(1)
	d.Finish(0, true, "", t0, 0, 0)
	if idx, st := d.Next(t0, func(ch string) bool { return ch == "c" }); idx != nil || st != DeliveryActive {
		t.Fatalf("b in flight: %v %v", idx, st)
	}
	d.Finish(1, false, "x", t0, 0, 0)
	if d.Status() == DeliveryDone {
		t.Fatal("c still owed")
	}
	d.Begin(2)
	d.Finish(2, true, "", t0, 0, 0)
	if d.Status() != DeliveryDone || d.Fallback() {
		t.Fatalf("all done: %v", d.Status())
	}
}

func TestDeliveryRestartRecovery(t *testing.T) {
	d := NewDelivery([]string{"feishu", "tg"}, DeliverFallback)
	d.Begin(0)
	d.Finish(0, false, "x", t0, 0, 0)
	d.Begin(1)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	// Crash here; reload.
	var back Delivery
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Recover()
	idx, _ := back.Next(t0, nil)
	if !reflect.DeepEqual(idx, []int{1}) || back.Pos() != 1 || back.Steps[1].Attempts != 1 {
		t.Fatalf("continue at chain_pos, not at the head: %v %+v", idx, back)
	}
}

func TestDeliveryEmptyChain(t *testing.T) {
	d := NewDelivery(nil, "")
	if st := d.Status(); st != DeliveryFailed {
		t.Fatalf("empty chain cannot deliver: %v", st)
	}
}

func TestLateBy(t *testing.T) {
	if LateBy(t0, t0.Add(4*time.Minute), 5*time.Minute) != 0 {
		t.Error("not late")
	}
	if got := LateBy(t0, t0.Add(12*time.Minute), 5*time.Minute); got != 12*time.Minute {
		t.Errorf("late %v", got)
	}
}
