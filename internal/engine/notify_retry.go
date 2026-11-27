package engine

import (
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/notify"
)

// permanentNotifyErr reports failures that must not be retried on the same
// channel: bad credentials, forbidden, or clearly invalid parameters.
// 429 is temporary (honour Retry-After). 5xx and network timeouts retry.
func permanentNotifyErr(att notify.Attempt) bool {
	switch att.Status {
	case 400, 401, 403, 404, 422:
		return true
	}
	err := strings.ToLower(att.Error)
	for _, p := range []string{
		"unauthorized", "forbidden", "invalid api", "invalid_token",
		"authentication", "bad credentials", "invalid access", "signature",
		"not configured", "channel not configured",
	} {
		if strings.Contains(err, p) {
			return true
		}
	}
	return false
}

// notifyRetryBackoff returns the wait before the next attempt. Attempts is
// the count after Begin (1 = first try failed → schedule first retry).
// With the default 2s base, the second retry waits 5s as requested.
func notifyRetryBackoff(attempts int, base time.Duration) time.Duration {
	if attempts <= 1 {
		return base
	}
	if base == 2*time.Second {
		return 5 * time.Second
	}
	return base
}
