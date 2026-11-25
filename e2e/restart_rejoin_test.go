//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The old master restarts after a standby took over: it finds the new
// master as it starts, steps down before acting as master, and joins it
// within seconds instead of waiting for the new master's notice.
func TestRestartedMasterRejoinsQuickly(t *testing.T) {
	// ip: the standby's address as it connects. name: the standby written
	// with a host name while every node listens on all addresses, as most
	// installations are set up.
	// lost: the old master no longer has the standby's identity (lost
	// with its data, or never saved); the cluster CA must be enough.
	for _, c := range []struct {
		name         string
		byName, lost bool
	}{{"ip", false, false}, {"name", true, false}, {"lost", false, true}} {
		t.Run(c.name, func(t *testing.T) { restartedMasterRejoins(t, c.byName, c.lost) })
	}
}

func restartedMasterRejoins(t *testing.T, byName, lost bool) {
	a := startOn(t, "a", fmt.Sprintf(soaRT, "a"), byName)
	a.setup("admin", "master-pass-1")
	b := startOn(t, "b", fmt.Sprintf(soaRT, "b"), byName)
	b.setup("local", "standby-pass-1")
	join(t, a, b, "bk")
	if byName {
		makeStandbyAt(t, a, b, "bk", fmt.Sprintf("https://localhost:%d", b.port))
	} else {
		makeStandby(t, a, b, "bk")
	}
	time.Sleep(6 * time.Second) // the master keeps its peers' identities within seconds
	a.stop()
	if lost {
		_ = os.Remove(filepath.Join(a.dir, "cluster-peer-identities.json"))
	}
	if code := b.call("POST", "/api/v1/cluster/promote", map[string]any{}, nil); code != 200 {
		t.Fatalf("promote: %d", code)
	}
	waitLong(t, "b master", 60*time.Second, func() bool { return b.isMaster() })
	a.cmd = nil
	t0 := time.Now()
	a.run()
	a.login("admin", "master-pass-1")
	d := waitLong(t, "a to rejoin b", 90*time.Second, func() bool { return a.joined(b, "a") })
	t.Logf("a rejoined b %s after its start (%s after it answered)", time.Since(t0).Round(100*time.Millisecond), d.Round(100*time.Millisecond))
	if time.Since(t0) > 15*time.Second {
		t.Errorf("rejoin took %s: want under 15 s", time.Since(t0).Round(time.Second))
	}
	if !a.logHas("stepping down at start") {
		t.Errorf("a did not find the new master as it started")
	}
}

func startOn(t *testing.T, name, bind string, all bool) *server {
	s := &server{t: t, name: name, dir: t.TempDir(), port: freePort(t), listenAll: all}
	if err := os.WriteFile(filepath.Join(s.dir, "monitors.conf"), []byte(bind), 0o600); err != nil {
		t.Fatal(err)
	}
	s.run()
	t.Cleanup(s.stop)
	return s
}
