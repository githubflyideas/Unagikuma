package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/store"
)

// A change is reminded before it starts (only by reminders that come
// after its announcement), holds its monitors in maintenance during the
// window, and says so when it overruns.
func TestChangeLifecycle(t *testing.T) {
	dir := t.TempDir()
	bind := `@ IN SOA t. admin.localhost. ( 1 1m 15s 5s 3 )
ops     IN CHANNEL log
default IN POLICY chain=ops
web     IN TCP 192.168.1.10:80 group=public
db      IN TCP 192.168.1.20:5432
`
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(bind), 0o600); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	t0 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	clock.Store(t0.UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	var logs bytes.Buffer
	var mu sync.Mutex
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run, Now: now, Location: time.UTC,
		Log: slog.New(slog.NewTextHandler(lockedWriter{&logs, &mu}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	sent := func(title string) bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(logs.String(), title)
	}

	start := t0.Add(25 * time.Hour)
	c, err := a.Store().SaveChange(store.Change{Title: "Core switch upgrade", Impact: "VPN down", Select: "name:web",
		Start: start, End: start.Add(2 * time.Hour), CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.NotifyChange(c, ChangeAnnounced, "admin", ""); err != nil {
		t.Fatal(err)
	}
	a.ChangesChanged()
	waitFor(t, "the announcement", 5*time.Second, func() bool { return sent("Planned change: Core switch upgrade") })

	step := func(to time.Time) {
		clock.Store(to.UnixNano())
		a.tickChanges()
	}
	step(start.Add(-24*time.Hour + time.Minute))
	waitFor(t, "the 24h reminder", 5*time.Second, func() bool { return sent("In 24h: Core switch upgrade") })
	step(start.Add(-time.Hour + time.Minute))
	waitFor(t, "the 1h reminder", 5*time.Second, func() bool { return sent("In 1h: Core switch upgrade") })
	step(start.Add(time.Minute))
	waitFor(t, "the start", 5*time.Second, func() bool { return sent("Change started: Core switch upgrade") })
	if c, _ = a.Store().Change(c.ID); c.State != store.ChangeRunning {
		t.Fatalf("state %s", c.State)
	}
	var inWindow bool
	for _, m := range a.Engine().Maintenance() {
		if m.Name == "change-1" {
			inWindow = m.Open && strings.Join(m.Monitors, ",") == "web"
		}
	}
	if !inWindow {
		t.Fatalf("no open maintenance window for web: %+v", a.Engine().Maintenance())
	}
	step(start.Add(2*time.Hour + time.Minute))
	waitFor(t, "the overdue notice", 5*time.Second, func() bool { return sent("Change overdue: Core switch upgrade") })
	step(start.Add(3 * time.Hour)) // nothing is sent twice
	mu.Lock()
	n := strings.Count(logs.String(), "In 24h: Core switch upgrade")
	mu.Unlock()
	if n != 1 {
		t.Fatalf("the 24h reminder went out %d times", n)
	}

	// Announced 30 minutes before: no reminders that would predate it.
	soon := now().Add(30 * time.Minute)
	c2, _ := a.Store().SaveChange(store.Change{Title: "Quick fix", Start: soon, End: soon.Add(time.Hour), CreatedBy: "admin"})
	step(soon.Add(-time.Minute))
	if c2, _ = a.Store().Change(c2.ID); len(c2.Sent) != 0 {
		t.Fatalf("reminders before the announcement: %v", c2.Sent)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{2 * time.Hour: "2h", 90 * time.Minute: "1h30m", 40 * time.Minute: "40m", 26 * time.Hour: "26h"} {
		if got := shortDuration(d); got != want {
			t.Errorf("%s: %s, want %s", d, got, want)
		}
	}
}
