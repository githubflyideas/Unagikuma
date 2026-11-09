package server

import (
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/config"
)

// Pause and resume on a joined replica must work locally: no shared-config
// 409, no relay to the master, and the master's monitors.conf stays clean.
func TestReplicaHTTPPauseIsSiteLocal(t *testing.T) {
	master := tlsEnv(t, testBind+fastLogin)
	var jc struct {
		Code string `json:"code"`
	}
	if master.do("POST", "/api/v1/cluster/join-codes", map[string]any{}, &jc) != 200 {
		t.Fatal("join code")
	}
	joiner := tlsEnv(t, `@ IN SOA box. admin.localhost. ( 2026102801 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s
`)
	if c := joiner.do("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": "bj", "title": "Beijing"}, nil); c != 200 {
		t.Fatalf("join: %d", c)
	}
	waitUntil(t, func() bool {
		var st app.AgentState
		joiner.do("GET", "/api/v1/cluster/agent", nil, &st)
		return st.Member != nil && st.Status != nil && st.Status.Connected
	})
	waitUntil(t, func() bool {
		cfg := joiner.app.EffectiveConfig()
		return cfg != nil && cfg.FindMonitor("web") != nil
	})

	// Other shared writes stay blocked.
	if c := joiner.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "x", "type": "PING", "target": "10.0.0.1"}}, nil); c != 409 {
		t.Fatalf("create on replica: %d (want 409)", c)
	}

	var pause struct {
		Changed   int  `json:"changed"`
		SiteLocal bool `json:"site_local"`
		Paused    bool `json:"paused"`
	}
	if c := joiner.do("POST", "/api/v1/monitors/web/pause", map[string]any{"paused": true}, &pause); c != 200 || !pause.SiteLocal {
		t.Fatalf("pause on replica: %d %+v", c, pause)
	}
	if !joiner.app.LocalPaused("web") {
		t.Fatal("replica overlay missing")
	}
	if master.app.LocalPaused("web") || master.app.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("master was affected by replica pause")
	}
	if strings.Contains(master.app.Config().Text, "disabled") {
		t.Fatal("master file gained disabled=")
	}

	// Master unreachable: leave the agent connected state alone but pause
	// must still work (overlay is local). Resume then pause again.
	if c := joiner.do("POST", "/api/v1/monitors/web/pause", map[string]any{"paused": false}, &pause); c != 200 {
		t.Fatalf("resume on replica: %d", c)
	}
	if joiner.app.LocalPaused("web") {
		t.Fatal("resume left overlay")
	}
	if c := joiner.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"web", "db"}, "paused": true}, &pause); c != 200 || pause.Changed != 2 {
		t.Fatalf("bulk pause on replica: %d %+v", c, pause)
	}

	if !joiner.app.LocalPaused("web") || !joiner.app.LocalPaused("db") {
		t.Fatal("bulk pause overlay missing")
	}
	// Standalone pause still works and does not write disabled=.
	solo := newEnv(t, testBind, nil)
	solo.setup()
	serial := solo.app.Config().Serial
	if c := solo.do("POST", "/api/v1/monitors/web/pause", map[string]any{"paused": true}, &pause); c != 200 || !pause.SiteLocal {
		t.Fatalf("standalone pause: %d %+v", c, pause)
	}
	if solo.app.Config().Serial != serial || strings.Contains(solo.app.Config().Text, "disabled") {
		t.Fatal("standalone pause wrote monitors.conf")
	}
}

func TestSharedDisabledResumeDoesNotWriteConfig(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	if _, err := e.app.EditConfigFrom("admin", "disable web+db", nil, func(f *config.File) error {
		if err := app.SetPaused(f, []string{"web"}, true); err != nil {
			return err
		}
		return app.SetPaused(f, []string{"db"}, true)
	}); err != nil {
		t.Fatal(err)
	}
	text := e.app.Config().Text
	serial := e.app.Config().Serial
	if !e.app.Config().Config.FindMonitor("web").Disabled || !e.app.Config().Config.FindMonitor("db").Disabled {
		t.Fatal("setup")
	}
	var out struct {
		Changed   int  `json:"changed"`
		SiteLocal bool `json:"site_local"`
		Paused    bool `json:"paused"`
	}
	if c := e.do("POST", "/api/v1/monitors/web/pause", map[string]any{"paused": false}, &out); c != 200 || !out.SiteLocal || out.Paused {
		t.Fatalf("resume: %d %+v", c, out)
	}
	if e.app.Config().Serial != serial || e.app.Config().Text != text || !e.app.Config().Config.FindMonitor("web").Disabled {
		t.Fatal("single resume wrote monitors.conf")
	}
	if e.app.EffectivelyPaused("web") {
		t.Fatal("web still effectively paused")
	}
	var bulk struct {
		Changed   int  `json:"changed"`
		SiteLocal bool `json:"site_local"`
	}
	if c := e.do("POST", "/api/v1/monitors/bulk-pause", map[string]any{"names": []string{"db"}, "paused": false}, &bulk); c != 200 || bulk.Changed != 1 || !bulk.SiteLocal {
		t.Fatalf("bulk resume: %d %+v", c, bulk)
	}
	if e.app.Config().Serial != serial || e.app.Config().Text != text || !e.app.Config().Config.FindMonitor("db").Disabled {
		t.Fatal("bulk resume wrote monitors.conf")
	}
	if e.app.EffectivelyPaused("db") || e.app.EffectivelyPaused("web") {
		t.Fatal("still effectively paused after local resumes")
	}
}
