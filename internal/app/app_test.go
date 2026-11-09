package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/store"
)

const testConfig = `@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s suspend.recheck-spread=0s notify.repeat=1m
logc    IN CHANNEL log
default IN POLICY chain=logc max=3
web     IN HTTP http://192.168.1.10/ title="内网站点"
dns     IN DNS  192.168.1.1
backup  IN PUSH period=1h
`

// fakeProbes decides results per monitor name.
type fakeProbes struct {
	mu   sync.Mutex
	fail map[string]bool
}

func (f *fakeProbes) set(name string, failing bool) {
	f.mu.Lock()
	f.fail[name] = failing
	f.mu.Unlock()
}

func (f *fakeProbes) run(_ context.Context, s probe.Spec) probe.Result {
	f.mu.Lock()
	failing := f.fail[s.Name]
	f.mu.Unlock()
	ev := &probe.Evidence{Peer: "192.168.1.10:80"}
	if failing {
		return probe.Result{Vote: probe.VoteFail, Reason: probe.ReasonConnRefused, Detail: "connection refused", Evidence: ev}
	}
	return probe.Result{Vote: probe.VoteOK, Reason: probe.ReasonOK, Latency: 3 * time.Millisecond, Evidence: ev}
}

func newApp(t *testing.T, text string) (*App, *fakeProbes, *safeBuffer, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProbes{fail: map[string]bool{}}
	logs := &safeBuffer{}
	lg := slog.New(slog.NewTextHandler(lockedWriter{&logs.b, &logs.mu}, nil))
	a, err := New(Options{DataDir: dir, Probe: fp.run, Log: lg, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return a, fp, logs, dir
}

// safeBuffer is a log buffer the test reads while the app writes to it.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func status(a *App, name string) string {
	r, ok := a.Engine().Row(name)
	if !ok {
		return ""
	}
	return r.Status
}

func TestEndToEndIncident(t *testing.T) {
	a, fp, logs, _ := newApp(t, testConfig)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	waitFor(t, "web ok", 5*time.Second, func() bool { return status(a, "web") == engine.StatusOK })
	if r, _ := a.Engine().Row("dns"); r.Type != "UDP" || r.Depth != "L3" {
		t.Fatalf("dns row %+v", r)
	}

	fp.set("web", true)
	waitFor(t, "web critical (HARD after 2 attempts)", 6*time.Second, func() bool {
		r, _ := a.Engine().Row("web")
		return r.Status == engine.StatusCritical && r.Hard && r.IncidentID != 0
	})
	waitFor(t, "problem notification delivered", 5*time.Second, func() bool {
		a.Store().Flush()
		p, _ := a.Store().Deliveries("", 0, 10)
		return len(p.Items) > 0 && p.Items[0].OK
	})
	mu := sync.Mutex{}
	mu.Lock()
	logged := logs.String()
	mu.Unlock()
	if !strings.Contains(logged, "内网站点") || !strings.Contains(logged, "notification") {
		t.Fatalf("log channel did not render the message:\n%s", logged)
	}
	ov := a.Engine().Overview()
	if ov.Unhandled != 1 || ov.Counts[engine.StatusCritical] != 1 {
		t.Fatalf("overview %+v", ov)
	}
	incs := a.Engine().OpenIncidents()
	if len(incs) != 1 {
		t.Fatalf("incidents %+v", incs)
	}
	if err := a.Engine().Ack(incs[0].ID, "alice", "on it", 0); err != nil {
		t.Fatal(err)
	}
	if ov := a.Engine().Overview(); ov.Unhandled != 0 {
		t.Fatalf("acked incident still unhandled: %+v", ov)
	}

	fp.set("web", false)
	waitFor(t, "web recovered", 5*time.Second, func() bool { return status(a, "web") == engine.StatusOK })
	waitFor(t, "recovery notification", 5*time.Second, func() bool {
		a.Store().Flush()
		p, _ := a.Store().Deliveries("", 0, 10)
		return len(p.Items) >= 2 && p.Items[0].Kind == "RECOVERY"
	})
	a.Store().Flush()
	ev, _ := a.Store().Events(store.EventFilter{Monitor: "web"})
	if len(ev.Items) < 3 {
		t.Fatalf("events %+v", ev.Items)
	}
	// Push monitor: a push makes it OK.
	if !a.Engine().Push("backup", true, "done", 0, "192.168.1.5") {
		t.Fatal("push not accepted")
	}
	if status(a, "backup") != engine.StatusOK {
		t.Fatalf("backup %s", status(a, "backup"))
	}
}

func TestConfigSaveWatchRollback(t *testing.T) {
	a, _, _, dir := newApp(t, testConfig)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()

	cur := a.Config()
	// Saving from the UI with the same serial bumps it.
	text := strings.Replace(cur.Text, `title="内网站点"`, `title="站点"`, 1)
	st, err := a.SaveConfig([]byte(text), cur.ETag(), "alice", "rename title")
	if err != nil {
		t.Fatal(err)
	}
	if st.Serial <= cur.Serial || !strings.Contains(st.Text, `title="站点"`) {
		t.Fatalf("serial %d → %d", cur.Serial, st.Serial)
	}
	if r, _ := a.Engine().Row("web"); r.Title != "站点" {
		t.Fatalf("title not applied: %+v", r)
	}
	// A stale ETag is a conflict.
	if _, err := a.SaveConfig([]byte(text), cur.ETag(), "bob", ""); err != ErrConflict {
		t.Fatalf("conflict: %v", err)
	}
	// Invalid text is rejected with diagnostics.
	if _, err := a.SaveConfig([]byte(st.Text+"bad IN HTTP ftp://x\n"), "", "bob", ""); err == nil {
		t.Fatal("invalid text accepted")
	} else if ie, ok := IsInvalid(err); !ok || len(ie.Diags) == 0 {
		t.Fatalf("%v", err)
	}

	// External edit without a serial bump: banner, not applied.
	path := filepath.Join(dir, "monitors.conf")
	edited := strings.Replace(st.Text, "dns     IN DNS  192.168.1.1", "dns     IN DNS  192.168.1.2", 1)
	os.WriteFile(path, []byte(edited), 0o600)
	waitFor(t, "serial banner", 5*time.Second, func() bool { b := a.Banner(); return b != nil && b.Code == "serial_not_bumped" })
	// The banner's button applies it with a bumped serial.
	st2, err := a.BumpSerial("alice", nil)
	if err != nil || st2.Serial <= st.Serial || a.Banner() != nil {
		t.Fatalf("bump: %v %d", err, st2.Serial)
	}
	// External edit with a higher serial is applied.
	f, _ := config.Parse(path, []byte(st2.Text))
	f.SetSerial(st2.Serial + 1)
	os.WriteFile(path, append(f.Bytes(), []byte("extra IN HTTP http://192.168.1.20/\n")...), 0o600)
	waitFor(t, "external edit applied", 5*time.Second, func() bool { _, ok := a.Engine().Row("extra"); return ok })

	// Rollback re-applies an old version under a new serial.
	before := a.Config().Serial
	st3, err := a.Rollback(cur.Serial, "alice", nil)
	if err != nil || st3.Serial <= before {
		t.Fatalf("rollback: %v %d", err, st3.Serial)
	}
	if _, ok := a.Engine().Row("extra"); ok {
		t.Fatal("rollback did not remove the monitor")
	}
	snaps, _ := a.Store().Snapshots(20)
	if len(snaps) < 4 {
		t.Fatalf("snapshots %+v", snaps)
	}
	// Tunables edited through the structured editor.
	if _, err := a.EditConfig("alice", "tunables", func(f *config.File) error {
		return f.SetTunables(map[string]string{"notify.max": "5"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if a.Config().Config.Tunables.Int("notify.max") != 5 {
		t.Fatal("tunable not applied")
	}
}

func TestSecretsResolution(t *testing.T) {
	text := strings.Replace(testConfig, "logc    IN CHANNEL log", "logc    IN CHANNEL log\nhook    IN CHANNEL webhook url=${secret:hook}", 1)
	text = strings.Replace(text, "default IN POLICY chain=logc", "default IN POLICY chain=logc,hook", 1)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "monitors.conf"), []byte(text), 0o600)
	if _, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run}); err == nil {
		t.Fatal("unknown secret must fail validation on first start")
	}
	// With the secret present, the reference validates and resolves.
	st, _ := store.Open(dir, store.Options{})
	kek, _ := store.LoadOrCreateKey(dir)
	st.Secrets(kek).Put("hook", "https://example.com/hook", nil)
	st.Close()
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatal(err)
	}
	ec := a.engineConfig(a.Config().Config)
	if ec.Channels["hook"].Opts["url"] != "https://example.com/hook" {
		t.Fatalf("%+v", ec.Channels["hook"])
	}
	a.Store().Close()
}

// A new installation writes the $FILE header; saving from the UI raises
// its revision in place and the file stays in the new form.
func TestStarterRevisionBump(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	cur := a.Config()
	text := cur.Text + "c IN CHANNEL log\n"
	st, err := a.SaveConfig([]byte(text), cur.ETag(), "alice", "add a channel")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "monitors.conf"))
	want := fmt.Sprintf("$FILE revision=%d master=unagikuma", st.Serial)
	if st.Serial <= cur.Serial || !strings.Contains(string(b), want) || strings.Contains(string(b), "SOA") {
		t.Fatalf("revision %d → %d:\n%s", cur.Serial, st.Serial, b)
	}
}

func TestStarterConfig(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{DataDir: dir, Probe: (&fakeProbes{fail: map[string]bool{}}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	b, err := os.ReadFile(filepath.Join(dir, "monitors.conf"))
	if err != nil || !strings.Contains(string(b), "$FILE revision=") {
		t.Fatalf("starter: %v %s", err, b)
	}
	if len(a.Config().Config.Monitors) != 0 {
		t.Fatal("starter has monitors")
	}
}

func TestEngineConfigNodes(t *testing.T) {
	ec := func(extra string) engine.Config {
		a, _, _, _ := newApp(t, testConfig+extra)
		defer a.Store().Close()
		return a.engineConfig(a.Config().Config)
	}
	find := func(c engine.Config, name string) engine.MonitorSpec {
		for _, m := range c.Monitors {
			if m.Name == name {
				return m
			}
		}
		t.Fatalf("monitor %s missing", name)
		return engine.MonitorSpec{}
	}

	// Without remote nodes nothing changes: single-machine judging.
	single := ec("")
	if single.Self != "" || len(single.Nodes) != 0 || find(single, "web").Nodes != nil {
		t.Fatalf("single machine: %+v", single)
	}

	c := ec(`bj   IN NODE "北京" region=north policy=default node-timeout=2m
sh   IN NODE "上海"
master-only IN PING 192.168.1.3 nodes=unagikuma
gw   IN PING 192.168.1.1 nodes=bj
ntp  IN NTP  192.168.1.2
`)
	if c.Self != "unagikuma" || len(c.Nodes) != 2 {
		t.Fatalf("nodes: %q %+v", c.Self, c.Nodes)
	}
	if n := c.Nodes[0]; n.Name != "bj" || n.Title != "北京" || n.Region != "north" || n.Policy != "default" || n.Timeout != 2*time.Minute || !n.Probe {
		t.Fatalf("bj: %+v", n)
	}
	if got := strings.Join(find(c, "web").Nodes, ","); got != "unagikuma,bj,sh" {
		t.Fatalf("default participants: %s", got)
	}
	if got := strings.Join(find(c, "master-only").Nodes, ","); got != "unagikuma" {
		t.Fatalf("master scope collapsed to all sites: %s", got)
	}
	if got := strings.Join(find(c, "gw").Nodes, ","); got != "bj" {
		t.Fatalf("named participants: %s", got)
	}
	if got := strings.Join(find(c, "backup").Nodes, ","); got != "unagikuma" {
		t.Fatal("push monitors are judged where the pushes arrive")
	}
	if !find(c, "ntp").TimeSensitive || find(c, "web").TimeSensitive {
		t.Fatal("time sensitivity")
	}
	if find(c, "web").PartialEscalate != 30*time.Minute {
		t.Fatalf("partial escalate: %v", find(c, "web").PartialEscalate)
	}

	// A master that only schedules leaves probing to the agents.
	c = ec(`unagikuma IN NODE "调度" probe=no
bj   IN NODE "北京"
`)
	if got := strings.Join(find(c, "web").Nodes, ","); got != "bj" {
		t.Fatalf("probe=no master: %s", got)
	}
}

func TestScopedMonitorsReachOnlyTheirNodes(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig+`bj IN NODE "北京" region=north
sh IN NODE "上海" region=east
$SCOPE region:north
bj-core IN PING 10.10.0.1
$SCOPE *
`)
	defer a.Store().Close()
	cfg := a.Config().Config
	ec := a.engineConfig(cfg)
	for _, m := range ec.Monitors {
		if m.Name == "bj-core" && strings.Join(m.Nodes, ",") != "bj" {
			t.Fatalf("bj-core nodes: %v", m.Nodes)
		}
	}
	has := func(node, mon string) bool {
		for _, m := range agentConfigs(cfg, ec)[node].Monitors {
			if m.Name == mon {
				return true
			}
		}
		return false
	}
	if !has("bj", "bj-core") || has("sh", "bj-core") || !has("sh", "web") {
		t.Fatal("regional monitors must reach only their region's agents")
	}
}

// A node of a cluster runs its master's configuration: edits made on it
// would never be checked, so they are refused.
func TestMemberRefusesConfigEdits(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	a.am.mu.Lock()
	a.am.member = &Membership{Server: "https://master:19110", Node: "b"}
	a.am.mu.Unlock()
	if _, err := a.SaveConfig([]byte(testConfig), "", "admin", ""); !errors.Is(err, ErrMember) {
		t.Fatalf("save: %v", err)
	}
	if _, err := a.BumpSerial("admin", nil); !errors.Is(err, ErrMember) {
		t.Fatalf("bump: %v", err)
	}
}
