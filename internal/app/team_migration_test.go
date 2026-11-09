package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file with policies is rewritten once to team channels: simple
// policies become the teams' channels and are deleted, policies with more
// stay, and the old file is kept.
func TestPoliciesMoveToTeamChannels(t *testing.T) {
	const old = `$FILE revision=1
mail    IN CHANNEL log
pager   IN CHANNEL log
default IN POLICY chain=mail
ops     IN POLICY chain=pager,mail mode=all
slow    IN POLICY chain=pager repeat=2h
web     IN TEAM managers=li policy=ops
db      IN TEAM viewers=wang policy=slow
gw      IN TCP 10.0.0.1:443
shop    IN TCP 10.0.0.2:443 team=web policy=ops
pg      IN TCP 10.0.0.3:5432 team=db
own     IN TCP 10.0.0.4:22 policy=system
`
	a, _, _, dir := newApp(t, old)
	t.Cleanup(func() { a.store.Close() }) // Windows cannot remove an open core.db
	text := a.Config().Text
	for _, want := range []string{
		"admin   IN TEAM channels=mail",
		"web     IN TEAM members=li channels=pager,mail mode=all",
		"mail    IN CHANNEL log system=yes",
		"slow    IN POLICY chain=pager repeat=2h",
		"db      IN TEAM policy=slow members=wang\n",
		"shop    IN TCP 10.0.0.2:443 team=web\n",
		"own     IN TCP 10.0.0.4:22 system-channel=yes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	for _, gone := range []string{"default IN POLICY", "ops     IN POLICY"} {
		if strings.Contains(text, gone) {
			t.Errorf("%q kept:\n%s", gone, text)
		}
	}
	cfg := a.Config().Config
	if m := cfg.FindMonitor("shop"); m.Policy != "team:web" {
		t.Errorf("shop policy %s", m.Policy)
	}
	if m := cfg.FindMonitor("pg"); m.Policy != "slow" {
		t.Errorf("pg policy %s", m.Policy)
	}
	if m := cfg.FindMonitor("gw"); m.Policy != "team:admin" {
		t.Errorf("gw policy %s", m.Policy)
	}
	if sys := cfg.FindPolicy("system"); sys == nil || strings.Join(sys.Chain, ",") != "mail" {
		t.Errorf("system: %+v", sys)
	}
	b, err := os.ReadFile(filepath.Join(dir, "monitors.conf.before-teams"))
	if err != nil || string(b) != old {
		t.Errorf("backup: %v %q", err, b)
	}
	// Once is enough: the result needs nothing more.
	a2, _, _, _ := newApp(t, text)
	t.Cleanup(func() { a2.store.Close() })
	if a2.Config().Text != text {
		t.Errorf("second run changed the file:\n%s", a2.Config().Text)
	}
}
