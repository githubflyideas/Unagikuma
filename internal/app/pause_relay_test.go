package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/engine"
)

func TestNodePauseIsWrittenOnTheMaster(t *testing.T) {
	m, _, _, _ := newApp(t, testConfig+"b IN NODE Beijing\nc IN NODE Tokyo\n")
	t.Cleanup(func() { m.store.Close() })
	b := observationSite(t, "b")
	if !b.IsMember() || m.IsMember() {
		t.Fatal("membership")
	}
	ms, bs := observationServer(t, m), observationServer(t, b)
	rememberObservationPeer(t, b, m, ms)
	rememberObservationPeer(t, m, b, bs)

	// The node itself cannot write its configuration.
	if _, err := b.EditConfig("alice", "x", nil); !errors.Is(err, ErrMember) {
		t.Fatalf("node wrote configuration: %v", err)
	}
	ctx := context.Background()
	if err := b.RelayPause(ctx, PauseRequest{Monitors: []string{"web"}, Paused: true, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if mon := m.Config().Config.FindMonitor("web"); mon == nil || !mon.Disabled {
		t.Fatal("not paused on the master")
	}
	if !strings.Contains(m.Config().Text, "disabled=yes") {
		t.Fatal(m.Config().Text)
	}
	if err := b.RelayPause(ctx, PauseRequest{Monitors: []string{"web"}, Paused: false, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if m.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("not resumed on the master")
	}
	if err := b.RelayPause(ctx, PauseRequest{Monitors: []string{"nope"}, Paused: true, Actor: "alice"}); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown monitor: %v", err)
	}
	// A master refuses a server it does not know as a node.
	stranger := observationSite(t, "c")
	rememberObservationPeer(t, stranger, m, ms)
	if err := stranger.RelayPause(ctx, PauseRequest{Monitors: []string{"web"}, Paused: true, Actor: "eve"}); err == nil {
		t.Fatal("unknown node paused a monitor")
	}
	if m.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("stranger's pause written")
	}
	ms.Close()
	if err := b.RelayPause(ctx, PauseRequest{Monitors: []string{"web"}, Paused: true, Actor: "alice"}); !errors.Is(err, ErrMasterUnavailable) {
		t.Fatalf("master down: %v", err)
	}
	if v, _ := m.Store().VerifyAudit(); !v.OK {
		t.Fatal("audit broken")
	}
}
