//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

func TestExecCheckRuns(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"DISK CRITICAL - /data 3% free|/data=97%;80;90\"\nexit 2\n"
	if err := os.WriteFile(filepath.Join(dir, "check_disk"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	probe.SetPluginDirs([]string{dir})
	t.Cleanup(func() { probe.SetPluginDirs(nil) })
	e := newEnv(t, `@ IN SOA unagikuma. admin.localhost. ( 2026102801 1s 500ms 300ms 1 )
$SET engine.first-round-delay=0s
disk IN EXEC check_disk args="-p /data" device=db1
`, nil)
	e.setup()
	waitUntil(t, func() bool {
		r, _ := e.app.Engine().Row("disk")
		return r.Status == "critical"
	})
	r, _ := e.app.Engine().Row("disk")
	if r.Reason != probe.ReasonPluginCritical || r.Detail != "DISK CRITICAL - /data 3% free" || r.Metrics["perf./data"] != 97 {
		t.Fatalf("row: %+v", r)
	}
	if rep := e.app.Engine().DailyReport(); rep.OK {
		t.Fatalf("report: %+v", rep)
	}
}

func TestScriptInventoryAndEnable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "check_ok"), []byte("#!/bin/sh\necho OK\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe.SetPluginDirs([]string{dir})
	t.Cleanup(func() { probe.SetPluginDirs(nil) })
	e := newEnv(t, `@ IN SOA unagikuma. admin.localhost. ( 2026102801 1m 15s 5s 2 )
running IN EXEC check_gone
`, nil)
	e.setup()
	var ov struct {
		Problems int `json:"script_problems"`
	}
	e.do("GET", "/api/v1/overview", nil, &ov)
	if ov.Problems != 1 {
		t.Fatalf("script problems: %d", ov.Problems)
	}
	// New script checks start paused.
	if c := e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "ok", "type": "EXEC", "target": "check_ok"}}, nil); c != 200 {
		t.Fatalf("create: %d", c)
	}
	if m := e.app.Config().Config.FindMonitor("ok"); m == nil || !m.Disabled {
		t.Fatalf("not paused: %+v", m)
	}
	if c := e.do("POST", "/api/v1/monitors/ok/pause", map[string]any{"paused": false}, nil); c != 200 {
		t.Fatalf("enable with the script in place: %d", c)
	}
	if c := e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "gone", "type": "EXEC", "target": "check_missing"}}, nil); c != 200 {
		t.Fatalf("create: %d", c)
	}
	var refused struct {
		Code  string
		Nodes []scriptCell
	}
	if c := e.do("POST", "/api/v1/monitors/gone/pause", map[string]any{"paused": false}, &refused); c != 409 || refused.Code != CodeScriptsMissing || len(refused.Nodes) != 1 {
		t.Fatalf("enable without the script: %d %+v", c, refused)
	}
	if c := e.do("POST", "/api/v1/monitors/gone/pause", map[string]any{"paused": false, "force": true}, nil); c != 200 {
		t.Fatalf("forced: %d", c)
	}
	var rep struct {
		Checks   []scriptCheck
		Problems int
	}
	e.do("GET", "/api/v1/scripts", nil, &rep)
	if rep.Problems != 2 || len(rep.Checks) != 3 || !rep.Checks[0].Problem {
		t.Fatalf("report: %+v", rep)
	}
}
