package engine

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
)

func TestDeliveryRetryAttemptCountAndPermanentSkip(t *testing.T) {
	d := NewDelivery([]string{"mail", "sms"}, DeliverFallback)
	now := time.Unix(0, 0).UTC()
	d.Begin(0)
	// temporary fail → retry
	d.Finish(0, false, "timeout", now, 2, notifyRetryBackoff(1, 2*time.Second))
	if d.Steps[0].State != StepPending || d.Steps[0].RetryAt.Sub(now) != 2*time.Second {
		t.Fatalf("pending %+v", d.Steps[0])
	}
	d.Begin(0)
	d.Finish(0, false, "timeout", now.Add(2*time.Second), 2, notifyRetryBackoff(2, 2*time.Second))
	if d.Steps[0].State != StepPending || d.Steps[0].RetryAt.Sub(now.Add(2*time.Second)) != 5*time.Second {
		t.Fatalf("second pending %+v", d.Steps[0])
	}
	d.Begin(0)
	d.Finish(0, false, "timeout", now.Add(10*time.Second), 2, 2*time.Second)
	if d.Steps[0].State != StepFailed {
		t.Fatalf("exhausted %+v", d.Steps[0])
	}

	d2 := NewDelivery([]string{"mail"}, DeliverAll)
	d2.Begin(0)
	att := notify.Attempt{Status: 401, Error: "unauthorized"}
	retries := 2
	if permanentNotifyErr(att) {
		retries = 0
	}
	d2.Finish(0, false, att.Error, now, retries, 2*time.Second)
	if d2.Steps[0].State != StepFailed {
		t.Fatalf("permanent should not retry: %+v", d2.Steps[0])
	}
}
