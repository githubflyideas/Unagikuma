package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file from before 2.6 (policies) goes through 2.6 (team channels) to
// 3.0 (alert channels and groups) at one start: everyone is notified as
// before, and the old files are kept.
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
		"mail    IN CHANNEL log system=yes",
		"slow    IN POLICY chain=pager repeat=2h",
		"own     IN TCP 10.0.0.4:22 system-channel=yes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, " TEAM ") || strings.Contains(text, "team=") {
		t.Errorf("teams kept:\n%s", text)
	}
	cfg := a.Config().Config
	for name, want := range map[string][2]string{"shop": {"web", "web"}, "pg": {"slow", "db"}, "gw": {"default", ""}} {
		if m := cfg.FindMonitor(name); m.Policy != want[0] || m.Group != want[1] {
			t.Errorf("%s: policy %s group %q, want %v", name, m.Policy, m.Group, want)
		}
	}
	if p := cfg.FindPolicy("default"); p == nil || strings.Join(p.Chain, ",") != "mail" {
		t.Errorf("default: %+v", p)
	}
	if p := cfg.FindPolicy("web"); p == nil || strings.Join(p.Chain, ",") != "pager,mail" || p.Mode != "all" {
		t.Errorf("web: %+v", p)
	}
	if sys := cfg.FindPolicy("system"); sys == nil || strings.Join(sys.Chain, ",") != "mail" {
		t.Errorf("system: %+v", sys)
	}
	b, err := os.ReadFile(filepath.Join(dir, "monitors.conf.before-teams"))
	if err != nil || string(b) != old {
		t.Errorf("backup: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "monitors.conf.before-groups")); err != nil {
		t.Errorf("groups backup: %v", err)
	}
	// Once is enough: the result needs nothing more.
	a2, _, _, _ := newApp(t, text)
	t.Cleanup(func() { a2.store.Close() })
	if a2.Config().Text != text {
		t.Errorf("second run changed the file:\n%s\n---- was:\n%s", a2.Config().Text, text)
	}
}
