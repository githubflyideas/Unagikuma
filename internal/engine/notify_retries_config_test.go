package engine

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/tunables"
)

func TestNotifyChannelRetriesDefaultIsTwo(t *testing.T) {
	set, err := tunables.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Int("notify.channel-retries") != 2 {
		t.Fatalf("default=%d", set.Int("notify.channel-retries"))
	}
}

func TestNotifyChannelRetriesExplicitZeroPreserved(t *testing.T) {
	set, err := tunables.New(map[string]string{"notify.channel-retries": "0"})
	if err != nil {
		t.Fatal(err)
	}
	if set.Int("notify.channel-retries") != 0 {
		t.Fatalf("explicit 0 got %d", set.Int("notify.channel-retries"))
	}
	d := NewDelivery([]string{"mail"}, DeliverAll)
	now := time.Unix(0, 0).UTC()
	d.Begin(0)
	d.Finish(0, false, "timeout", now, set.Int("notify.channel-retries"), 2*time.Second)
	if d.Steps[0].State != StepFailed {
		t.Fatalf("explicit 0 must not retry: %+v", d.Steps[0])
	}
}
