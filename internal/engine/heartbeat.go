package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Heartbeat calls an external URL periodically (a dead man's switch such as
// Healthchecks.io or another unagikuma's PUSH monitor). When this machine
// dies, loses power or its uplink, the calls stop and the external service
// raises the alarm — on a single machine the only real-time warning about
// the monitor itself. The URL is read on every beat so it can be changed at
// runtime (setup wizard, settings page) without a restart.
type Heartbeat struct {
	Client   *http.Client
	URL      func() string        // "" = disabled
	Interval func() time.Duration // heartbeat.interval
	Timeout  func() time.Duration // heartbeat.timeout
	Now      func() time.Time
	// UserAgent identifies the instance, e.g. "unagikuma/home-nas".
	UserAgent string

	mu     sync.Mutex
	status HeartbeatStatus
}

// HeartbeatStatus feeds the "this machine" health card.
type HeartbeatStatus struct {
	Configured bool
	Target     string // redacted URL (scheme://host/…)
	LastOK     time.Time
	LastError  string
	LastTry    time.Time
}

// Status returns the latest status.
func (h *Heartbeat) Status() HeartbeatStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// Run beats until ctx is done. The first beat is immediate so a freshly
// configured URL shows green right away.
func (h *Heartbeat) Run(ctx context.Context) {
	for {
		h.Beat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(h.Interval()):
		}
	}
}

// Beat performs one call (exported for "test now" in the UI).
func (h *Heartbeat) Beat(ctx context.Context) {
	raw := h.URL()
	now := h.Now()
	h.mu.Lock()
	h.status.Configured = raw != ""
	h.status.Target = RedactURL(raw)
	h.mu.Unlock()
	if raw == "" {
		return
	}
	err := h.call(ctx, raw)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.LastTry = now
	if err != nil {
		h.status.LastError = err.Error()
		return
	}
	h.status.LastOK, h.status.LastError = now, ""
}

func (h *Heartbeat) call(ctx context.Context, raw string) error {
	ctx, cancel := context.WithTimeout(ctx, h.Timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return fmt.Errorf("invalid heartbeat URL")
	}
	if h.UserAgent != "" {
		req.Header.Set("User-Agent", h.UserAgent)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		// The URL usually embeds a secret check id: never echo it.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("heartbeat to %s failed: %v", RedactURL(raw), err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("heartbeat to %s: HTTP %d", RedactURL(raw), resp.StatusCode)
	}
	return nil
}

// RedactURL keeps only scheme and host of a URL that may embed a secret
// (token in the path, query or userinfo).
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	s := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		s += "/…"
	}
	return s
}
