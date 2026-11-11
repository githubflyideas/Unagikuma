//go:build e2e

// Package e2e runs the built program as separate processes on 127.0.0.1:
// go test -tags e2e ./e2e/
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "unagikuma-e2e")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "unagikuma")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "../cmd/unagikuma")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// server is one running unagikuma server.
type server struct {
	t    *testing.T
	name string
	dir  string
	port int
	cmd  *exec.Cmd
	log  *bytes.Buffer
	c    *http.Client
	csrf string
	// listenAll listens on every address (":port"), as most installations do.
	listenAll bool
}

func start(t *testing.T, name, bind string) *server {
	s := &server{t: t, name: name, dir: t.TempDir(), port: freePort(t)}
	if err := os.WriteFile(filepath.Join(s.dir, "monitors.conf"), []byte(bind), 0o600); err != nil {
		t.Fatal(err)
	}
	s.run()
	t.Cleanup(s.stop)
	return s
}

func (s *server) run() {
	s.log = &bytes.Buffer{}
	host := "127.0.0.1"
	if s.listenAll {
		host = ""
	}
	s.cmd = exec.Command(bin, "server", "--data", s.dir, "--listen", fmt.Sprintf("%s:%d", host, s.port))
	s.cmd.Stdout, s.cmd.Stderr = s.log, s.log
	if err := s.cmd.Start(); err != nil {
		s.t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	// 30 s: a manual promote waits a random 1–10 s on purpose before it
	// answers, and a 10 s timeout failed the test whenever it drew near 10.
	s.c = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	s.waitFor("healthz", func() bool {
		resp, err := s.c.Get(s.url("/healthz"))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil && resp.StatusCode == 200
	})
}

func (s *server) stop() {
	if s.cmd != nil && s.cmd.Process != nil && s.cmd.ProcessState == nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	if s.t.Failed() {
		s.t.Logf("--- %s log ---\n%s", s.name, s.log.String())
	}
}

func (s *server) url(p string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", s.port, p) }

func (s *server) waitFor(what string, cond func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			s.t.Fatalf("%s: timed out waiting for %s", s.name, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *server) call(method, path string, body, out any) int {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.url(path), rd)
	req.Header.Set("Content-Type", "application/json")
	if s.csrf != "" {
		req.Header.Set("X-Unagikuma-CSRF", s.csrf)
	}
	resp, err := s.c.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	if resp.StatusCode >= 400 {
		s.t.Logf("%s %s %s → %d %s", s.name, method, path, resp.StatusCode, data)
	}
	return resp.StatusCode
}

func (s *server) setup(user, pass string) {
	s.t.Helper()
	var r struct{ CSRF string }
	if code := s.call("POST", "/api/v1/setup", map[string]string{"username": user, "password": pass}, &r); code != 200 {
		s.t.Fatalf("%s setup: %d", s.name, code)
	}
	s.csrf = r.CSRF
}

func (s *server) login(user, pass string) bool {
	jar, _ := cookiejar.New(nil)
	s.c.Jar, s.csrf = jar, ""
	var r struct{ CSRF string }
	if s.call("POST", "/api/v1/session", map[string]string{"username": user, "password": pass}, &r) != 200 {
		return false
	}
	s.csrf = r.CSRF
	return true
}

func (s *server) monitors() []string {
	var r struct {
		Items []struct{ Name, Status string }
	}
	s.call("GET", "/api/v1/monitors", nil, &r)
	var out []string
	for _, m := range r.Items {
		out = append(out, m.Name+"="+m.Status)
	}
	return out
}

const soa = "@ IN SOA %s. admin.localhost. ( 2026102801 2s 1s 1s 1 )\n$SET engine.first-round-delay=0s cluster.standby-sync=30s cluster.standby-retry=5s\n"

// The standby takes over shared configuration but retains its own users.
// The former master rejoins under its original site name and keeps checking.
func TestFailover(t *testing.T) {
	a := start(t, "master", fmt.Sprintf(soa, "a"))
	a.setup("admin", "master-pass-1")
	if code := a.call("POST", "/api/v1/monitors", map[string]any{"record": map[string]string{
		"name": "self", "type": "TCP", "target": fmt.Sprintf("127.0.0.1:%d", a.port)}}, nil); code != 200 {
		t.Fatalf("add monitor: %d", code)
	}

	b := start(t, "standby", fmt.Sprintf(soa, "b"))
	b.setup("local", "standby-pass-1")
	var jc struct{ Code string }
	if code := a.call("POST", "/api/v1/cluster/join-codes", map[string]string{"server": fmt.Sprintf("https://127.0.0.1:%d", a.port)}, &jc); code != 200 {
		t.Fatalf("join code: %d", code)
	}
	if code := b.call("POST", "/api/v1/cluster/join", map[string]any{"code": jc.Code, "node": "bk", "title": "Standby"}, nil); code != 200 {
		t.Fatalf("join: %d", code)
	}
	a.waitFor("node bk online", func() bool {
		var r struct {
			Nodes []struct {
				Name   string
				Online bool
			}
		}
		a.call("GET", "/api/v1/nodes", nil, &r)
		for _, n := range r.Nodes {
			if n.Name == "bk" && n.Online {
				return true
			}
		}
		return false
	})
	if code := a.call("PUT", "/api/v1/nodes/bk", map[string]any{"title": "Standby", "standby": fmt.Sprintf("https://127.0.0.1:%d", b.port)}, nil); code != 200 {
		t.Fatalf("set standby: %d", code)
	}
	b.waitFor("a copy of the master's state", func() bool {
		var r struct{ Standby struct{ Size int64 } }
		b.call("GET", "/api/v1/cluster/agent", nil, &r)
		return r.Standby.Size > 0
	})

	// The master is lost.
	a.stop()
	if code := b.call("POST", "/api/v1/cluster/promote", map[string]any{}, nil); code != 200 {
		t.Fatalf("promote: %d", code)
	}
	b.waitFor("the standby to restart with configuration authority", func() bool {
		var r struct{ Mode string }
		return b.call("GET", "/api/v1/config", nil, &r) == 200 && r.Mode == "rw"
	})
	b.waitFor("the new master to retain its local admin", func() bool { return b.login("local", "standby-pass-1") })
	b.waitFor("the master's monitor checked by the new master", func() bool {
		for _, m := range b.monitors() {
			if strings.HasPrefix(m, "self=") {
				return true
			}
		}
		return false
	})
	var ov struct{ Term uint64 }
	b.call("GET", "/api/v1/overview", nil, &ov)
	if ov.Term < 1 {
		t.Fatalf("term after takeover: %d", ov.Term)
	}

	// The old master comes back and learns it was replaced.
	a.cmd = nil
	a.run()
	if !a.login("admin", "master-pass-1") {
		t.Fatal("old master login")
	}
	a.waitFor("the old master to rejoin under its original site name", func() bool {
		var r struct {
			Member *struct{ Node string }
			Status *struct{ Connected bool }
		}
		a.call("GET", "/api/v1/cluster/agent", nil, &r)
		return r.Member != nil && r.Member.Node == "a" && r.Status != nil && r.Status.Connected
	})
}
