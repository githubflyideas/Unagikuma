//go:build !windows

package probe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func plugin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestExecPlugins(t *testing.T) {
	dir := t.TempDir()
	SetPluginDirs([]string{dir})
	t.Cleanup(func() { SetPluginDirs(nil) })
	plugin(t, dir, "check_ok", `echo "DISK OK - free space: / 3326 MB (56%)|/=2643MB;5948;5958;0;5968 'inode use'=12%;80;90"; exit 0`)
	plugin(t, dir, "check_warn", `echo "LOAD WARNING - load average: 5.1"; exit 1`)
	plugin(t, dir, "check_crit", `echo "PROCS CRITICAL: 0 processes with command name 'nginx'"; exit 2`)
	plugin(t, dir, "check_unknown", `echo "UNKNOWN - cannot read /proc"; exit 3`)
	plugin(t, dir, "check_args", `echo "got $1 $2 [$3] env=$UNAGIKUMA_ADDRESS secret=${SECRET_FROM_SERVER:-none}"; exit 0`)
	plugin(t, dir, "check_hang", `sleep 30 & sleep 30`)
	if err := os.WriteFile(filepath.Join(dir, "not_exec"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECRET_FROM_SERVER", "leaked")

	run := func(target string, o map[string]string) Result {
		return Run(context.Background(), Spec{Name: "t", Type: "EXEC", Target: target, Opts: o, Timeout: 2 * time.Second})
	}
	r := run("check_ok", nil)
	if r.Vote != VoteOK || r.Reason != ReasonOK || r.Detail != "DISK OK - free space: / 3326 MB (56%)" {
		t.Fatalf("ok: %+v", r)
	}
	if r.Metrics["perf./"] != 2643 || r.Metrics["perf.inode_use"] != 12 {
		t.Fatalf("perfdata: %v", r.Metrics)
	}
	if r := run("check_warn", nil); r.Vote != VoteOK || !r.Degraded || r.Reason != ReasonPluginWarning {
		t.Fatalf("warning: %+v", r)
	}
	if r := run("check_crit", nil); r.Vote != VoteFail || r.Reason != ReasonPluginCritical || r.Detail != "PROCS CRITICAL: 0 processes with command name 'nginx'" {
		t.Fatalf("critical: %+v", r)
	}
	if r := run("check_unknown", nil); r.Vote != VoteAbstain || r.Reason != ReasonPluginUnknown {
		t.Fatalf("unknown: %+v", r)
	}
	r = run(`check_args -H $HOSTADDRESS$`, map[string]string{"args": `"two words"`, "address": "10.0.0.5"})
	if r.Detail != "got -H 10.0.0.5 [two words] env=10.0.0.5 secret=none" {
		t.Fatalf("args/env: %q", r.Detail)
	}
	start := time.Now()
	r = Run(context.Background(), Spec{Name: "t", Type: "EXEC", Target: "check_hang", Timeout: 300 * time.Millisecond})
	if r.Vote != VoteFail || r.Reason != ReasonPluginTimeout || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout: %+v after %s", r, time.Since(start))
	}
	for _, target := range []string{"../bin/sh", "/bin/sh", "check_missing", "not_exec"} {
		if r := run(target, nil); r.Vote != VoteAbstain || r.Reason != ReasonConfig {
			t.Errorf("%s must not run: %+v", target, r)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	got, err := splitArgs(`-w 20% -c "10 %" 'a b' c\ d -p "x\"y"`)
	want := []string{"-w", "20%", "-c", "10 %", "a b", "c d", "-p", `x"y`}
	if err != nil || len(got) != len(want) {
		t.Fatalf("%q %v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%d: %q != %q", i, got[i], want[i])
		}
	}
	if _, err := splitArgs(`"open`); err == nil {
		t.Fatal("unterminated quote accepted")
	}
	if err := Validate(Spec{Type: "EXEC", Target: "check_x", Opts: map[string]string{"args": "'"}}); err == nil {
		t.Fatal("bad args accepted")
	}
}

func TestInitScriptDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "check-scripts")
	if err := InitScriptDir(dir); err != nil {
		t.Fatal(err)
	}
	SetPluginDirs([]string{dir})
	t.Cleanup(func() { SetPluginDirs(nil) })
	if l := Scripts(); len(l) != 1 || l[0].Name != "check_example" || !l[0].Exec || l[0].Hash == "" {
		t.Fatalf("inventory: %+v", l)
	}
	r := Run(context.Background(), Spec{Name: "t", Type: "EXEC", Target: "check_example", Opts: map[string]string{"args": "-w 101 -c 102"}, Timeout: 5 * time.Second})
	if r.Vote != VoteOK || r.Metrics["perf.used"] <= 0 {
		t.Fatalf("example: %+v", r)
	}
	// An existing directory is left alone.
	os.Remove(filepath.Join(dir, "README.txt"))
	if err := InitScriptDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.txt")); err == nil {
		t.Fatal("an existing directory was rewritten")
	}
}

// A relative script directory (server started with --data ./data) still
// finds its scripts, which run with their own directory as working one.
func TestRelativePluginDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "check_rel"), []byte("#!/bin/sh\necho REL OK\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(dir))
	SetPluginDirs([]string{"./" + filepath.Base(dir)})
	t.Cleanup(func() { SetPluginDirs(nil) })
	if d := PluginDirs()[0]; !filepath.IsAbs(d) {
		t.Fatalf("plugin dir kept relative: %s", d)
	}
	r := Run(context.Background(), Spec{Name: "rel", Type: "EXEC", Target: "check_rel", Timeout: 5 * time.Second})
	if r.Vote != VoteOK || r.Detail != "REL OK" {
		t.Fatalf("relative dir: %+v", r)
	}
}
