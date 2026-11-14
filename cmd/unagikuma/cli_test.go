//go:build !windows

package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// capture runs f with stdout and stderr redirected, and returns both.
func capture(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	ferr := f()
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-done, ferr
}

// withStdin runs f with s as standard input (a pipe, not a terminal).
func withStdin(t *testing.T, s string, f func() error) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = w.WriteString(s); w.Close() }()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	return f()
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// One installation goes through the server and every maintenance command:
// started, signed up, stopped by SIGTERM, then checked, formatted, backed
// up, restored, its password reset and its audit log verified and exported.
func TestCommandsOnARealInstallation(t *testing.T) {
	dir := t.TempDir()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) }))
	defer target.Close()
	conf := fmt.Sprintf("$FILE revision=2026111101\n$SET engine.first-round-delay=0s localnet.gateway-monitors=off\nweb 10s IN HTTP %s/\ndown 10s IN TCP 127.0.0.1:1 attempts=1\n", target.URL)
	cfg := filepath.Join(dir, "monitors.conf")
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	logFile := filepath.Join(dir, "server.log")
	served := make(chan error, 1)
	go func() {
		served <- dispatch("server", []string{"--data", dir, "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--log-file", logFile, "--trusted-proxy", "10.0.0.0/8", "--instance", "cli-test"})
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	up := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && !up; time.Sleep(100 * time.Millisecond) {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			up = resp.StatusCode == 200
		}
	}
	if !up {
		t.Fatalf("server did not start: %s", readFile(logFile))
	}
	resp, err := http.Post(base+"/api/v1/setup", "application/json", strings.NewReader(`{"username":"admin","password":"Admin-pass-1"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("setup: %v %v", resp, err)
	}
	resp.Body.Close()
	time.Sleep(1500 * time.Millisecond) // a round, so there is history and an incident
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not stop on SIGTERM")
	}

	run := func(cmd string, args ...string) (string, error) {
		return capture(t, func() error { return dispatch(cmd, args) })
	}
	if out, err := run("check", cfg); err != nil || !strings.Contains(out, "OK") && !strings.Contains(out, "ok") && out != "" {
		t.Logf("check: %q %v", out, err)
	}
	if _, err := run("check", "--json", "--data", dir, cfg); err != nil {
		t.Errorf("check --json: %v", err)
	}
	bad := filepath.Join(dir, "bad.conf")
	_ = os.WriteFile(bad, []byte("$FILE revision=1\nx IN HTTP\n"), 0o600)
	if _, err := run("check", bad); err == nil {
		t.Error("check accepted a broken file")
	}
	if _, err := run("fmt", "--check", cfg); err != nil {
		if _, err := run("fmt", cfg); err != nil {
			t.Errorf("fmt: %v", err)
		}
	}
	unparsable := filepath.Join(dir, "unparsable.conf")
	_ = os.WriteFile(unparsable, []byte("$FILE revision=1\nx IN HTTP \"unterminated\n"), 0o600)
	if _, err := run("fmt", unparsable); err == nil {
		t.Error("fmt accepted a file it cannot parse")
	}
	if out, err := run("serial", cfg); err != nil || !strings.Contains(out, "2026111101") {
		t.Errorf("serial: %q %v", out, err)
	}
	if _, err := run("serial", "bump", cfg); err != nil {
		t.Errorf("serial bump: %v", err)
	}
	if out, _ := run("keywords"); !strings.Contains(strings.ToLower(out), "ntp") {
		t.Errorf("keywords: %q", out)
	}
	if out, err := run("probe", target.URL+"/"); err != nil || !strings.Contains(out, "ok") {
		t.Errorf("probe: %q %v", out, err)
	}
	if _, err := run("probe"); err == nil {
		t.Error("probe without a target")
	}
	if out, err := run("node", "list", "--data", dir); err != nil {
		t.Errorf("node list: %q %v", out, err)
	}
	if _, err := run("node", "revoke", "ghost", "--data", dir); err == nil {
		t.Error("revoke of an unknown node succeeded")
	}
	if _, err := run("node", "add", "x", "--data", dir); err == nil {
		t.Error("node add is not a command any more")
	}

	// Backup, restore (flags after the archive), wrong passphrase.
	pass := filepath.Join(dir, "..", "pass.txt")
	_ = os.WriteFile(pass, []byte("correct horse battery staple\n"), 0o600)
	arch := filepath.Join(t.TempDir(), "b.ukbackup")
	if out, err := run("backup", "--data", dir, "--passphrase-file", pass, "--history", "--out", arch); err != nil {
		t.Fatalf("backup: %q %v", out, err)
	}
	into := filepath.Join(t.TempDir(), "restored")
	if out, err := run("restore", arch, "--data", into, "--passphrase-file", pass); err != nil {
		t.Fatalf("restore: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(into, "core.db")); err != nil {
		t.Fatal("restore wrote no database")
	}
	wrong := filepath.Join(dir, "..", "wrong.txt")
	_ = os.WriteFile(wrong, []byte("not the passphrase\n"), 0o600)
	if _, err := run("restore", arch, "--data", filepath.Join(t.TempDir(), "x"), "--passphrase-file", wrong); err == nil {
		t.Error("restore with a wrong passphrase succeeded")
	}
	if _, err := run("restore", arch, "--data", into, "--passphrase-file", pass); err == nil {
		t.Error("restore over an installation without --force succeeded")
	}

	// Password from a pipe, then a generated one.
	err = withStdin(t, "New-pass-2026\n", func() error {
		_, err := run("reset-password", "--data", dir, "admin")
		return err
	})
	if err != nil {
		t.Errorf("reset-password from stdin: %v", err)
	}
	if out, err := run("reset-password", "--generate", "--revoke-tokens", "--data", dir, "admin"); err != nil || !strings.Contains(out, "admin") {
		t.Errorf("reset-password --generate: %q %v", out, err)
	}
	if _, err := run("reset-password", "--data", filepath.Join(dir, "nothing-here")); err == nil {
		t.Error("reset-password created an installation")
	}

	// Audit log.
	if out, err := run("audit", "verify", "--data", dir); err != nil || !strings.Contains(strings.ToLower(out), "entries") {
		t.Errorf("audit verify: %q %v", out, err)
	}
	for _, f := range []string{"csv", "json"} {
		if out, err := run("audit", "export", "--format", f, "--since", "7d", "--data", dir); err != nil || len(out) == 0 {
			t.Errorf("audit export %s: %v", f, err)
		}
	}
	if out, _ := run("audit"); !strings.Contains(out, "verify") {
		t.Errorf("audit usage: %q", out)
	}

	// Reports: usage, a bad format, a missing incident.
	if out, _ := run("report"); !strings.Contains(out, "incident") {
		t.Errorf("report usage: %q", out)
	}
	if _, err := run("report", "incident", "--format", "yaml", "--data", dir, "1"); err == nil {
		t.Error("report accepted format yaml")
	}
	if _, err := run("report", "incident", "--data", dir, "99999"); err == nil {
		t.Error("report of a missing incident succeeded")
	}
	if out, err := run("report", "incident", "--format", "json", "--data", dir, "1"); err == nil && !strings.Contains(out, "{") {
		t.Errorf("report json: %q", out)
	}

	// A master is no standby: promote is refused.
	if _, err := run("promote", "--data", dir); err == nil {
		t.Error("promote of a server without a standby copy succeeded")
	}
	if out, _ := run("help"); !strings.Contains(out, "unagikuma") {
		t.Errorf("help: %q", out)
	}
	if out, _ := run("version"); !strings.Contains(out, "unagikuma") {
		t.Errorf("version: %q", out)
	}
}

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestQuickLine(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com": "probe IN HTTP https://example.com",
		"example.com/health":  "probe IN PING example.com/health",
		"db:5432":             "probe IN TCP db:5432",
		"udp 10.0.0.1:ntp":    "probe IN UDP 10.0.0.1:ntp",
		"10.0.0.1:ntp":        "probe IN UDP 10.0.0.1:ntp",
		"ping 192.168.1.1":    "probe IN PING 192.168.1.1",
		"https example.com":   "probe IN HTTP https://example.com",
		"tcp db:5432 tls=yes": "probe IN TCP db:5432 tls=yes",
		"dns example.com x=1": "probe IN DNS example.com x=1",
	} {
		if got := quickLine(in); got != want {
			t.Errorf("quickLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	if ipn, err := parseCIDRs("10.0.0.0/8, 192.168.1.0/24"); err != nil || ipn == nil || !ipn(net.ParseIP("10.1.2.3")) || ipn(net.ParseIP("8.8.8.8")) {
		t.Errorf("parseCIDRs: %v", err)
	}
	for _, bad := range []string{"not-an-address", "192.168.1.1"} {
		if _, err := parseCIDRs(bad); err == nil {
			t.Errorf("parseCIDRs accepted %q", bad)
		}
	}
	t.Setenv("UK_TEST_ENV", "x")
	if envOr("UK_TEST_ENV", "d") != "x" || envOr("UK_TEST_UNSET", "d") != "d" {
		t.Error("envOr")
	}
	if configFile([]string{"a.conf"}) != "a.conf" {
		t.Error("configFile")
	}
	if l, err := newLogger("", slog.LevelDebug); err != nil || l == nil {
		t.Errorf("newLogger: %v", err)
	}
	if l, err := newLogger(filepath.Join(t.TempDir(), "x.log"), slog.LevelInfo); err != nil || l == nil {
		t.Errorf("newLogger to a file: %v", err)
	}
	if line, err := readTTYLine(strings.NewReader("secret\r\nrest")); err != nil || string(line) != "secret" {
		t.Errorf("readTTYLine: %q %v", line, err)
	}
	if line, err := readTTYLine(strings.NewReader("no newline")); err != nil || string(line) != "no newline" {
		t.Errorf("readTTYLine at EOF: %q %v", line, err)
	}
	if _, err := readTTYLine(strings.NewReader("")); err == nil {
		t.Error("readTTYLine of nothing")
	}
	if isTerminal(int(os.Stdin.Fd())) {
		t.Log("stdin is a terminal here")
	}
	if w := termWidth(77); w != 77 {
		t.Errorf("termWidth(77) = %d", w)
	}
}
