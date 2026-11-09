package engine

import (
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
)

func TestPermanentNotifyErr(t *testing.T) {
	if !permanentNotifyErr(notify.Attempt{Status: 401, Error: "nope"}) {
		t.Fatal("401")
	}
	if permanentNotifyErr(notify.Attempt{Status: 500, Error: "boom"}) {
		t.Fatal("500 should retry")
	}
	if permanentNotifyErr(notify.Attempt{Status: 429, Error: "slow down"}) {
		t.Fatal("429 should retry")
	}
	if !permanentNotifyErr(notify.Attempt{Error: "unauthorized token"}) {
		t.Fatal("auth string")
	}
}

func TestNotifyRetryBackoffSchedule(t *testing.T) {
	if d := notifyRetryBackoff(1, 2*time.Second); d != 2*time.Second {
		t.Fatalf("first %v", d)
	}
	if d := notifyRetryBackoff(2, 2*time.Second); d != 5*time.Second {
		t.Fatalf("second %v", d)
	}
	if d := notifyRetryBackoff(2, 3*time.Second); d != 3*time.Second {
		t.Fatalf("custom base kept %v", d)
	}
}

func TestRetryAfterDuration(t *testing.T) {
	if d := retryAfterDuration("Retry-After: 3"); d != 3*time.Second {
		t.Fatalf("got %v", d)
	}
	if d := retryAfterDuration("7"); d != 7*time.Second {
		t.Fatalf("got %v", d)
	}
}
