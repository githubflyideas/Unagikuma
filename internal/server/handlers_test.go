package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/license"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

const testBind = `@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s suspend.recheck-spread=0s
web     IN HTTP http://192.168.1.10/ title="Web site" group=public
backup  IN PUSH period=1h
db      IN TCP 192.168.1.20:5432
main    IN STATUSPAGE "Service status" select="group:public"
`

type env struct {
	t    *testing.T
	app  *app.App
	srv  *Server
	ts   *httptest.Server
	c    *http.Client
	csrf string
}

func okProbe(ctx context.Context, s probe.Spec) probe.Result {
	if s.Type == "EXEC" {
		return probe.Run(ctx, s) // local plugins, no network
	}
	if strings.HasSuffix(s.Target, ":1") { // port 1: a target that is down
		return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Latency: time.Millisecond, Evidence: &probe.Evidence{Peer: s.Target}}
	}
	return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Latency: 2 * time.Millisecond, Evidence: &probe.Evidence{Peer: s.Target}}
}

func newEnv(t *testing.T, bind string, ui fstest.MapFS) *env {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(bind), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ring := license.SingleKey(testLicenseKey.Public().(ed25519.PublicKey))
	a, err := app.New(app.Options{DataDir: dir, Probe: okProbe, Log: log, Instance: "test", LicenseRing: &ring})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	o := Options{Log: log}
	if ui != nil {
		o.Web = ui
	}
	srv := New(a, o)
	ts := httptest.NewServer(srv.Handler())
	jar, _ := cookiejar.New(nil)
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
		cancel()
		<-done
	})
	return &env{t: t, app: a, srv: srv, ts: ts, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

// do sends a request and decodes a JSON reply into out (when non-nil).
func (e *env) do(method, path string, body any, out any, hdr ...string) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.csrf != "" {
		req.Header.Set("X-Unagikuma-CSRF", e.csrf)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	return resp.StatusCode
}

func (e *env) setup() {
	e.t.Helper()
	var r struct{ CSRF string }
	if code := e.do("POST", "/api/v1/setup", map[string]string{"username": "admin", "password": "secret123", "lang": "en"}, &r); code != 200 {
		e.t.Fatalf("setup: %d", code)
	}
	e.csrf = r.CSRF
}

func TestSetupShownDespiteAnOldSessionCookie(t *testing.T) {
	e := newEnv(t, testBind, nil)
	u, _ := url.Parse(e.ts.URL)
	e.c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "left-by-an-earlier-installation", Path: "/"}})
	var er struct{ Code string }
	if code := e.do("GET", "/api/v1/session", nil, &er); code != 401 || er.Code != CodeSetupRequired {
		t.Fatalf("with an old cookie: %d %s", code, er.Code)
	}
	e.setup()
	e.c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "left-by-an-earlier-installation", Path: "/"}})
	if code := e.do("GET", "/api/v1/session", nil, &er); code != 401 || er.Code != CodeUnauthorized {
		t.Fatalf("after setup: %d %s", code, er.Code)
	}
}

func TestSetupLoginCSRF(t *testing.T) {
	e := newEnv(t, testBind, nil)
	var er struct{ Code string }
	if code := e.do("GET", "/api/v1/overview", nil, &er); code != 401 || er.Code != CodeSetupRequired {
		t.Fatalf("before setup: %d %s", code, er.Code)
	}
	var st struct{ Needed, Local bool }
	e.do("GET", "/api/v1/setup/status", nil, &st)
	if !st.Needed || !st.Local {
		t.Fatalf("status %+v", st)
	}
	// From another machine the setup token is required.
	h := e.srv.Handler()
	remote := func(body string) int {
		req := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(body))
		req.RemoteAddr = "192.0.2.7:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := remote(`{"password":"secret123"}`); code != 403 {
		t.Fatalf("remote setup without token: %d", code)
	}
	// A reverse proxy on this host relays from loopback: still remote.
	req := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(`{"password":"secret123"}`))
	req.RemoteAddr = "[::1]:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("proxied setup without token: %d", rec.Code)
	}
	if code := remote(`{"password":"secret123","token":"` + e.srv.SetupToken() + `"}`); code != 200 {
		t.Fatalf("remote setup with token: %d", code)
	}
	if e.srv.SetupToken() != "" {
		t.Fatal("setup token must be cleared")
	}
	if code := e.do("POST", "/api/v1/setup", map[string]string{"password": "other12345"}, nil); code != 409 {
		t.Fatalf("second setup: %d", code)
	}
	if code := e.do("POST", "/api/v1/session", map[string]string{"username": "admin", "password": "wrong-password"}, nil); code != 401 {
		t.Fatalf("bad login: %d", code)
	}
	var sess struct{ User, CSRF string }
	if code := e.do("POST", "/api/v1/session", map[string]string{"username": "admin", "password": "secret123"}, &sess); code != 200 || sess.CSRF == "" {
		t.Fatalf("login: %d %+v", code, sess)
	}
	// The overview always carries this host's path for the diagram.
	var ov struct {
		Paths []struct {
			Name   string `json:"name"`
			Self   bool   `json:"self"`
			Online bool   `json:"online"`
			Uplink string `json:"uplink"`
		} `json:"paths"`
	}
	if code := e.do("GET", "/api/v1/overview", nil, &ov); code != 200 || len(ov.Paths) != 1 || !ov.Paths[0].Self || !ov.Paths[0].Online || ov.Paths[0].Uplink == "" {
		t.Fatalf("overview: %d %+v", code, ov.Paths)
	}
	// Writes need the CSRF header.
	if code := e.do("POST", "/api/v1/incidents/bulk", map[string]any{"ids": []int{}, "action": "ack"}, nil); code != 403 {
		t.Fatalf("write without CSRF: %d", code)
	}
	e.csrf = sess.CSRF
	if code := e.do("POST", "/api/v1/incidents/bulk", map[string]any{"ids": []int{}, "action": "ack"}, nil); code != 200 {
		t.Fatalf("write with CSRF: %d", code)
	}
	if code := e.do("DELETE", "/api/v1/session", nil, nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code := e.do("GET", "/api/v1/overview", nil, nil); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
}

func TestTokenScopes(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	mk := func(scopes ...string) string {
		var r struct{ Token string }
		if code := e.do("POST", "/api/v1/tokens", map[string]any{"name": strings.Join(scopes, "-"), "scopes": scopes}, &r); code != 200 {
			t.Fatalf("token: %d", code)
		}
		return r.Token
	}
	read, write := mk("read"), mk("write")
	bare := &env{t: t, ts: e.ts, c: &http.Client{Timeout: 10 * time.Second}}
	auth := func(tok string) []string { return []string{"Authorization", "Bearer " + tok} }
	if code := bare.do("GET", "/api/v1/monitors", nil, nil, auth(read)...); code != 200 {
		t.Fatalf("read token list: %d", code)
	}
	rec := map[string]any{"record": map[string]any{"name": "api", "type": "TCP", "target": "192.168.1.30:22"}}
	if code := bare.do("POST", "/api/v1/monitors", rec, nil, auth(read)...); code != 403 {
		t.Fatalf("read token write: %d", code)
	}
	// Tokens are not subject to CSRF.
	if code := bare.do("POST", "/api/v1/monitors", rec, nil, auth(write)...); code != 200 {
		t.Fatalf("write token write: %d", code)
	}
	if code := bare.do("GET", "/api/v1/tokens", nil, nil, auth(write)...); code != 403 {
		t.Fatalf("write token admin: %d", code)
	}
	if code := bare.do("GET", "/api/v1/monitors", nil, nil, auth("uk_bogus")...); code != 401 {
		t.Fatalf("bogus token: %d", code)
	}
}

func TestMonitorsAndConfig(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	// Creating a monitor while the engine runs must not deadlock on the
	// live event that the state change publishes.
	var cr struct {
		Serial uint32
		ETag   string
	}
	rec := map[string]any{"record": map[string]any{"name": "api", "type": "HTTP", "target": "https://192.168.1.31/health", "interval": "5s", "opts": map[string]string{"title": "API"}}}
	if code := e.do("POST", "/api/v1/monitors", rec, &cr); code != 200 || cr.Serial <= 2026102301 {
		t.Fatalf("create: %d %+v", code, cr)
	}
	if code := e.do("POST", "/api/v1/monitors", rec, nil); code != 409 {
		t.Fatalf("duplicate: %d", code)
	}
	var list struct {
		Items []struct{ Name, Title, Status string }
		Total int
	}
	waitUntil(t, func() bool {
		e.do("GET", "/api/v1/monitors?q=name:api", nil, &list)
		return len(list.Items) == 1 && list.Items[0].Status == "ok"
	})
	if list.Items[0].Title != "API" {
		t.Fatalf("list %+v", list)
	}
	var detail map[string]any
	if code := e.do("GET", "/api/v1/monitors/api", nil, &detail); code != 200 {
		t.Fatalf("detail: %d", code)
	}
	if code := e.do("GET", "/api/v1/monitors/nope", nil, nil); code != 404 {
		t.Fatalf("missing detail: %d", code)
	}

	var cfg struct {
		Text, ETag string
		Serial     uint32
	}
	e.do("GET", "/api/v1/config", nil, &cfg)
	if !strings.Contains(cfg.Text, "api IN HTTP") && !strings.Contains(cfg.Text, "api") {
		t.Fatalf("config text lacks the new record:\n%s", cfg.Text)
	}
	newText := strings.Replace(cfg.Text, "db      IN TCP 192.168.1.20:5432\n", "", 1)
	if code := e.do("PUT", "/api/v1/config", map[string]string{"text": newText}, nil, "If-Match", `"1-000000000000"`); code != 412 {
		t.Fatalf("stale If-Match: %d", code)
	}
	var put struct{ Serial uint32 }
	if code := e.do("PUT", "/api/v1/config", map[string]string{"text": newText, "message": "drop db"}, &put, "If-Match", cfg.ETag); code != 200 || put.Serial <= cfg.Serial {
		t.Fatalf("save: %d %+v", code, put)
	}
	var bad struct {
		Code     string
		Location *Location
	}
	if code := e.do("PUT", "/api/v1/config", map[string]string{"text": newText + "x IN BOGUS y\n"}, &bad); code != 400 || bad.Location == nil {
		t.Fatalf("invalid save: %d %+v", code, bad)
	}
	var val struct {
		Diagnostics []map[string]any
		Changes     []map[string]any
	}
	e.do("POST", "/api/v1/config/validate", map[string]string{"text": newText + "extra IN PING 192.168.1.99\n"}, &val)
	if len(val.Changes) != 1 {
		t.Fatalf("validate: %+v", val)
	}
	var hist struct{ Items []map[string]any }
	e.do("GET", "/api/v1/config/history", nil, &hist)
	if len(hist.Items) < 2 {
		t.Fatalf("history: %+v", hist)
	}
	if code := e.do("POST", "/api/v1/config/rollback", map[string]any{"serial": cfg.Serial}, nil); code != 200 {
		t.Fatalf("rollback: %d", code)
	}
	if e.app.Config().Config.FindMonitor("db") == nil {
		t.Fatal("rollback did not restore db")
	}
	if code := e.do("DELETE", "/api/v1/monitors/api", nil, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if e.app.Config().Config.FindMonitor("api") != nil {
		t.Fatal("api still configured")
	}
	var qa struct {
		Records []struct{ Name, Type, Bind string }
	}
	e.do("POST", "/api/v1/quickadd/parse", map[string]string{"text": "https://example.org\n10.0.0.5:5432"}, &qa)
	if len(qa.Records) != 2 || qa.Records[0].Type != "HTTP" || qa.Records[1].Type != "TCP" {
		t.Fatalf("quickadd: %+v", qa)
	}
}

func TestPushBadgeStatusPage(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	var p struct{ Token, Path string }
	if code := e.do("GET", "/api/v1/monitors/backup/push", nil, &p); code != 200 || p.Token == "" {
		t.Fatalf("push token: %d", code)
	}
	anon := &env{t: t, ts: e.ts, c: &http.Client{Timeout: 10 * time.Second}}
	var pr struct{ OK bool }
	if code := anon.do("GET", p.Path+"?status=up&msg=done&ping=12", nil, &pr); code != 200 || !pr.OK {
		t.Fatalf("push: %d", code)
	}
	if code := anon.do("GET", "/api/push/nope", nil, nil); code != 404 {
		t.Fatalf("unknown push token: %d", code)
	}
	waitUntil(t, func() bool {
		r, ok := e.app.Engine().Row("backup")
		return ok && r.Status == "ok"
	})
	waitUntil(t, func() bool {
		r, ok := e.app.Engine().Row("web")
		return ok && r.Status == "ok"
	})

	resp, err := http.Get(e.ts.URL + "/badge/web/status.svg")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/svg+xml" || !bytes.Contains(body, []byte("Web site")) {
		t.Fatalf("badge: %d %s", resp.StatusCode, body)
	}
	// db is not on any status page: no badge, so names cannot be probed.
	if resp, _ := http.Get(e.ts.URL + "/badge/db/status.svg"); resp.StatusCode != 404 {
		t.Fatalf("unpublished badge: %d", resp.StatusCode)
	}

	var sp struct {
		Title    string
		Status   string
		Monitors []map[string]any
	}
	if code := anon.do("GET", "/api/v1/status/main", nil, &sp); code != 200 {
		t.Fatalf("status page: %d", code)
	}
	if sp.Title != "Service status" || len(sp.Monitors) != 1 || sp.Monitors[0]["title"] != "Web site" {
		t.Fatalf("status page: %+v", sp)
	}
	raw, _ := json.Marshal(sp.Monitors)
	if bytes.Contains(raw, []byte("192.168.1.10")) {
		t.Fatalf("status page leaks targets: %s", raw)
	}
	if code := anon.do("GET", "/api/v1/status/other", nil, nil); code != 404 {
		t.Fatalf("unknown status page: %d", code)
	}
}

func TestStaticUI(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html>index")},
		"status.html":      {Data: []byte("<!doctype html>status")},
		"vendor/preact.js": {Data: []byte("export{}")},
	}
	e := newEnv(t, testBind, ui)
	get := func(path string) (int, string, http.Header) {
		resp, err := http.Get(e.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	if code, body, h := get("/"); code != 200 || !strings.Contains(body, "index") || h.Get("Content-Security-Policy") == "" {
		t.Fatalf("/: %d %q %v", code, body, h)
	}
	if code, body, _ := get("/status/main"); code != 200 || !strings.Contains(body, "status") {
		t.Fatalf("status page html: %d %q", code, body)
	}
	if code, _, h := get("/vendor/preact.js"); code != 200 || !strings.Contains(h.Get("Cache-Control"), "max-age") {
		t.Fatalf("vendor: %d %v", code, h)
	}
	if code, _, _ := get("/nope.js"); code != 404 {
		t.Fatalf("missing file: %d", code)
	}
	if code, body, _ := get("/api/v1/nope"); code != 404 || !strings.Contains(body, "not_found") {
		t.Fatalf("unknown api: %d %s", code, body)
	}
	if code, body, _ := get("/api/v1/i18n/zh"); code != 200 || !strings.Contains(body, "status.") {
		t.Fatalf("catalog: %d %.80s", code, body)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// testLicenseKey signs licences for tests; envs accept it instead of the
// release keys.
var testLicenseKey = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))

// register installs a licence granting features.
func (e *env) register(features ...string) {
	e.t.Helper()
	f := license.Sign(testLicenseKey, license.File{Customer: "Test KK", Licensee: license.LicenseeOrganization, Product: license.Product,
		License: license.TypePerpetual, Tier: license.TierStandard, Features: features})
	b, _ := json.Marshal(f)
	if _, err := e.app.License().Install(b, time.Now()); err != nil {
		e.t.Fatal(err)
	}
}
