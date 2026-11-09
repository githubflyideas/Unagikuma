package config

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

const singleMachine = `@   IN  SOA  master.  me.example.com. ( 2026102101 1m 15s 5s 3 )

tg          IN CHANNEL telegram token=${secret:tg_token} chat=12345678
default     IN POLICY  chain=tg repeat=30m max=3

blog        IN HTTP https://blog.example.com title="博客" cert-days=14     ; wan
nas         IN HTTP http://192.168.1.10:5000 title="家里 NAS"             ; lan
router-dns  IN DNS  192.168.1.1 title="路由器 DNS"
backup      IN PUSH period=1d grace=2h title="夜间备份"
`

const multiSite = `; ================================================================
;  production
; ================================================================
@   IN  SOA  master.  ops.example.com. (
        2026102101 ; serial
        30s        ; interval
        10s        ; retry
        5s         ; timeout
        3          ; attempts
    )
$QUORUM  majority
$DIALECT huawei
$TAG site:bj color=#2f80ed
$SET notify.max=5 localnet.min-domains=4

;; ---- nodes ----
bj   IN NODE "北京联通机房"  anchors=lan:10.10.0.1;wan:223.5.5.5
sh   IN NODE "上海电信机房"  anchors=lan:10.20.0.1;wan:223.5.5.5

;; ---- channels ----
feishu-ops  IN CHANNEL feishu   url=${secret:feishu_hook} secret=${secret:feishu_sign}
tg-oncall   IN CHANNEL telegram token=${secret:tg_token} chat=-1001234567
default     IN POLICY  chain=feishu-ops,tg-oncall repeat=10m max=3 recovery=yes
system      IN POLICY  chain=feishu-ops repeat=30m max=2

$DEFAULT tags=env:prod policy=default
portal   1m IN HTTP https://portal.example.com title="官网首页" expect=200 keyword="登录" cert-days=14 degraded=800ms scope=wan
core-dns         IN UDP 10.0.0.53:dns      title="核心 DNS" query=corp.example.com qtype=SOA authorized=yes
core-ntp         IN UDP 10.0.0.1:ntp       max-offset=200ms authorized=yes
wan-ike       5m IN UDP 203.0.113.1:isakmp ike=v2 scope=wan
dnsix-thing      IN UDP 10.0.0.2:dnsix
db-primary       IN TCP  10.3.0.10:5432 title="主数据库"
core-sw-ping     IN PING 10.1.1.1 loss=20% rtt=50ms
public  IN STATUSPAGE "Example 服务状态" select="tag:public" windows=24h,7d,30d,90d
`

func load(t *testing.T, src string) (*File, *Config, []Diagnostic) {
	t.Helper()
	return Load("monitors.conf", []byte(src), Options{})
}

func errorsOf(ds []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, d := range ds {
		if d.Severity == SevError {
			out = append(out, d)
		}
	}
	return out
}

func TestExamplesParse(t *testing.T) {
	for name, src := range map[string]string{"single": singleMachine, "multi": multiSite} {
		f, cfg, ds := load(t, src)
		if e := errorsOf(ds); len(e) != 0 {
			t.Fatalf("%s: unexpected errors: %v", name, e)
		}
		if string(f.Bytes()) != src {
			t.Fatalf("%s: round trip changed the file", name)
		}
		if cfg.Serial != 2026102101 {
			t.Fatalf("%s: serial %d", name, cfg.Serial)
		}
	}
}

func TestSingleMachineModel(t *testing.T) {
	_, cfg, _ := load(t, singleMachine)
	if cfg.MName != "master" || cfg.RName != "me@example.com" {
		t.Fatalf("soa: %q %q", cfg.MName, cfg.RName)
	}
	d := cfg.Defaults
	if d.Interval != time.Minute || d.Retry != 15*time.Second || d.Timeout != 5*time.Second || d.Attempts != 3 {
		t.Fatalf("defaults %+v", d)
	}
	blog := cfg.FindMonitor("blog")
	if blog.Title != "博客" || blog.CertDays != 14 || blog.Type != "HTTP" || blog.Interval != time.Minute || blog.Policy != "default" {
		t.Fatalf("blog %+v", blog)
	}
	dns := cfg.FindMonitor("router-dns")
	if dns.Type != "UDP" || dns.Written != "DNS" || dns.UDP == nil || dns.UDP.Port != 53 || dns.UDP.Template != "dns" || dns.Target != "192.168.1.1:53" {
		t.Fatalf("dns %+v %+v", dns, dns.UDP)
	}
	push := cfg.FindMonitor("backup")
	if push.Type != "PUSH" || push.Period != 24*time.Hour || push.Grace != 2*time.Hour {
		t.Fatalf("push %+v", push)
	}
	if p := cfg.FindPolicy("system"); p == nil || !p.Implicit || len(p.Chain) != 1 || p.Chain[0] != "tg" {
		t.Fatalf("implicit system policy inherits default: %+v", p)
	}
	if ch := cfg.FindChannel("tg"); ch.Kind != "telegram" || ch.Opts["chat"] != "12345678" || len(ch.Refs) != 1 {
		t.Fatalf("channel %+v", ch)
	}
}

func TestMultiSiteModel(t *testing.T) {
	_, cfg, ds := load(t, multiSite)
	if cfg.Tunables.Int("notify.max") != 5 || cfg.Tunables.Int("localnet.min-domains") != 4 {
		t.Fatal("$SET not applied")
	}
	if p := cfg.FindPolicy("default"); p.Max != 3 || p.Repeat != 10*time.Minute {
		t.Fatalf("policy %+v", p)
	}
	portal := cfg.FindMonitor("portal")
	if portal.Interval != time.Minute || portal.Degraded != 800*time.Millisecond || portal.Scope != "wan" || len(portal.Tags) != 1 || portal.Tags[0].String() != "env:prod" {
		t.Fatalf("portal %+v", portal)
	}
	if ike := cfg.FindMonitor("wan-ike"); ike.UDP.Keyword != "isakmp" || ike.UDP.Port != 500 || ike.Interval != 5*time.Minute {
		t.Fatalf("ike %+v %+v", ike, ike.UDP)
	}
	if x := cfg.FindMonitor("dnsix-thing"); x.UDP.Port != 90 {
		t.Fatalf("dnsix under huawei: %+v", x.UDP)
	}
	if n := cfg.Nodes[0]; n.Description != "北京联通机房" || n.AnchorsLAN[0] != "10.10.0.1" || n.AnchorsWAN[0] != "223.5.5.5" {
		t.Fatalf("node %+v", n)
	}
	if sp := cfg.StatusPages[0]; sp.Title != "Example 服务状态" || len(sp.Windows) != 4 || sp.Path != "/status/public" {
		t.Fatalf("status page %+v", sp)
	}
	// core-ntp has no title: defaults to its name.
	if m := cfg.FindMonitor("core-ntp"); m.Title != "core-ntp" || m.UDP.Template != "ntp" {
		t.Fatalf("ntp %+v", m)
	}
	for _, d := range ds {
		if d.Code == "plaintext_secret" {
			t.Fatalf("references are not plaintext: %v", d)
		}
	}
}

func TestDiagnostics(t *testing.T) {
	soa := "@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n"
	cases := []struct {
		src, code string
		line      int
	}{
		{"web IN HTTP https://x\n", "soa_first", 1},
		{soa + "web IN HTTP ftp://x\n", "invalid_target", 2},
		{soa + "a IN HTTP https://x\na IN TCP h:1\n", "duplicate_name", 3},
		{soa + "a IN UDP h:dnsix\n", "invalid_keyword", 2},
		{soa + "$DIALECT asa\na IN UDP h:dns\n", "invalid_keyword", 3},
		{soa + "a IN HTTP https://x policy=nope\n", "unknown_policy", 2},
		{soa + "p IN POLICY chain=nope\n", "unknown_channel", 2},
		{soa + "a IN HTTP https://x scope=moon\n", "invalid_value", 2},
		{soa + "$SET notify.max=-1\n", "invalid_tunable", 2},
		{soa + "$SET nope=1\n", "invalid_tunable", 2},
		{soa + "a IN HTTP https://x\n$QUORUM all\n", "directive_order", 3},
		{soa + "c IN CHANNEL webhook url=${env:HOME}\n", "env_not_allowed", 2},
		{soa + "a IN MAINT\n", "missing_option", 2},
		{soa + "$INCLUDE x\n", "unsupported", 2},
		{soa + "a IN HTTP \"https://x\n", "unterminated_string", 2},
		{"@ IN SOA m. a.b. ( 1 1m 15s 5s 3\n", "unbalanced_paren", 1},
		{soa + "a IN FOO x\n", "unknown_type", 2},
		{soa + "-bad IN HTTP https://x\n", "invalid_name", 2},
		{soa + "a IN HTTP https://x title=1 title=2\n", "duplicate_option", 2},
		{soa + "p IN STATUSPAGE x select=status:critical\n", "invalid_select", 2},
		{"@ IN SOA m. a.b. ( 0 1m 15s 5s 3 )\n", "invalid_serial", 1},
		{soa + "p IN POLICY chain=c last-resort=stderr\nc IN CHANNEL log\n", "not_allowed", 2},
	}
	for _, c := range cases {
		_, _, ds := load(t, c.src)
		found := false
		for _, d := range ds {
			if d.Code == c.code && d.Pos.Line == c.line {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want %s at line %d, got %v", c.src, c.code, c.line, ds)
		}
	}
}

func TestWarnings(t *testing.T) {
	soa := "@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n"
	cases := map[string]string{
		soa + "c IN CHANNEL telegram token=123:abc chat=1\n":       "plaintext_secret",
		soa + "c IN CHANNEL feishu url=https://open.feishu.cn/x\n": "plaintext_secret",
		soa + "a IN UDP h:nameserver\n":                            "keyword_l0",
		soa + "a IN UDP h:ntp\n":                                   "reflective_unauthorized",
		soa + "a IN UDP h:snmptrap\n":                              "", // stateful with default interval 15m: no clamp needed
		soa + "a 10s IN UDP h:isakmp\n":                            "stateful_interval",
		soa + "a IN UDP h:1812\n":                                  "keyword_hint",
	}
	for src, code := range cases {
		_, cfg, ds := load(t, src)
		if e := errorsOf(ds); len(e) > 0 {
			t.Errorf("%q: errors %v", src, e)
			continue
		}
		if code == "" {
			continue
		}
		found := false
		for _, d := range ds {
			found = found || d.Code == code
		}
		if !found {
			t.Errorf("%q: want warning %s, got %v", src, code, ds)
		}
		if code == "stateful_interval" && cfg.Monitors[0].Interval != time.Minute {
			t.Errorf("stateful clamp: %v", cfg.Monitors[0].Interval)
		}
	}
}

func TestDefaultIsPositionalAndReplaces(t *testing.T) {
	src := `@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )
c IN CHANNEL log
core IN POLICY chain=c
a IN HTTP https://a
$DEFAULT policy=core tags=env:prod
b IN HTTP https://b
$DEFAULT group=web
d IN HTTP https://d tags=x
`
	_, cfg, ds := load(t, src)
	if e := errorsOf(ds); len(e) > 0 {
		t.Fatal(e)
	}
	a, b, d := cfg.FindMonitor("a"), cfg.FindMonitor("b"), cfg.FindMonitor("d")
	if a.Policy != "default" || len(a.Tags) != 0 {
		t.Errorf("before $DEFAULT: %+v", a)
	}
	if b.Policy != "core" || b.Tags[0].String() != "env:prod" {
		t.Errorf("$DEFAULT applied: %+v", b)
	}
	if d.Policy != "default" || d.Group != "web" || d.Tags[0].Key != "x" {
		t.Errorf("$DEFAULT replaced, explicit wins: %+v", d)
	}
}

func TestValidateHooks(t *testing.T) {
	src := "@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\nc IN CHANNEL feishu url=${secret:x}\na IN HTTP https://a expect=abc\n"
	_, _, ds := Load("f", []byte(src), Options{
		ValidateMonitor: func(m *Monitor) error {
			if m.Opts["expect"] == "abc" {
				return errors.New("bad expect")
			}
			return nil
		},
		ValidateChannel: func(kind string, opts map[string]string) error { return errors.New("no secret support") },
		SecretKnown:     func(string) bool { return false },
	})
	codes := map[string]bool{}
	for _, d := range ds {
		codes[d.Code] = true
	}
	for _, c := range []string{"invalid_option", "invalid_channel", "unknown_secret"} {
		if !codes[c] {
			t.Errorf("missing %s in %v", c, ds)
		}
	}
}

func TestRoundTripRandom(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	alphabet := []string{"a", " ", "\t", ";", "\"", "(", ")", "=", "\n", "\r\n", "IN", "HTTP", "x:1", "\\", "中"}
	for i := 0; i < 2000; i++ {
		var b strings.Builder
		for j := 0; j < r.Intn(60); j++ {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		src := b.String()
		f, _ := Parse("x", []byte(src))
		if string(f.Bytes()) != src {
			t.Fatalf("round trip failed for %q", src)
		}
		f.Config(Options{}) // must not panic
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte(singleMachine))
	f.Add([]byte(multiSite))
	f.Fuzz(func(t *testing.T, src []byte) {
		file, _ := Parse("x", src)
		if string(file.Bytes()) != string(src) {
			t.Fatal("round trip")
		}
		file.Config(Options{})
	})
}

// changedLines returns the line numbers (1-based, of a) that differ.
func changedLines(a, b string) int {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	n := 0
	for i := 0; i < max(len(al), len(bl)); i++ {
		if i >= len(al) || i >= len(bl) || al[i] != bl[i] {
			n++
		}
	}
	return n
}

func TestSetSerial(t *testing.T) {
	for _, src := range []string{singleMachine, multiSite, strings.ReplaceAll(multiSite, "\n", "\r\n")} {
		f, _ := Parse("x", []byte(src))
		if err := f.SetSerial(2026102201); err != nil {
			t.Fatal(err)
		}
		if s, _ := f.Serial(); s != 2026102201 {
			t.Fatalf("serial %d", s)
		}
		if n := changedLines(src, string(f.Bytes())); n != 1 {
			t.Fatalf("changed %d lines", n)
		}
		if !strings.Contains(string(f.Bytes()), "2026102201 ; serial") && strings.Contains(src, "; serial") {
			t.Fatal("comment lost")
		}
	}
}

func TestUpsertDeleteRename(t *testing.T) {
	f, _ := Parse("x", []byte(multiSite))
	// Replace in place keeps the line count and the other lines.
	err := f.UpsertRecord(RecordSpec{Name: "db-primary", Type: "TCP", Target: "10.3.0.11:5432", Opts: []Opt{{Key: "title", Value: "主数据库 B"}}})
	if err != nil {
		t.Fatal(err)
	}
	if n := changedLines(multiSite, string(f.Bytes())); n != 1 {
		t.Fatalf("upsert changed %d lines", n)
	}
	if !strings.Contains(string(f.Bytes()), `db-primary       IN TCP  10.3.0.11:5432 title="主数据库 B"`) {
		t.Fatal(string(f.Bytes()))
	}
	// Insert after the last monitor.
	if err := f.UpsertRecord(RecordSpec{Name: "new-web", Type: "HTTP", Target: "https://new.example.com"}); err != nil {
		t.Fatal(err)
	}
	text := string(f.Bytes())
	if strings.Index(text, "new-web") < strings.Index(text, "core-sw-ping") || strings.Index(text, "new-web") > strings.Index(text, "public  IN STATUSPAGE") {
		t.Fatalf("insert position:\n%s", text)
	}
	if err := f.RenameRecord("new-web", "newer-web"); err != nil {
		t.Fatal(err)
	}
	if err := f.RenameRecord("newer-web", "portal"); err == nil {
		t.Fatal("rename onto existing name")
	}
	if err := f.DeleteRecord("newer-web"); err != nil {
		t.Fatal(err)
	}
	if _, cfg, ds := Load("x", f.Bytes(), Options{}); len(errorsOf(ds)) != 0 || cfg.FindMonitor("newer-web") != nil {
		t.Fatalf("after edits: %v", ds)
	}
}

func TestDeleteKeepsSectionComments(t *testing.T) {
	src := `@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )
;; ---- web ----

; the blog
blog IN HTTP https://blog.example.com
; the shop, attached
shop IN HTTP https://shop.example.com
`
	f, _ := Parse("x", []byte(src))
	if err := f.DeleteRecord("shop"); err != nil {
		t.Fatal(err)
	}
	got := string(f.Bytes())
	if strings.Contains(got, "the shop") || !strings.Contains(got, ";; ---- web ----") || !strings.Contains(got, "; the blog") {
		t.Fatalf("%s", got)
	}
	if err := f.DeleteRecord("blog"); err != nil {
		t.Fatal(err)
	}
	if got = string(f.Bytes()); !strings.Contains(got, ";; ---- web ----") || strings.Contains(got, "the blog") {
		t.Fatalf("%s", got)
	}
}

func TestSetTunables(t *testing.T) {
	f, _ := Parse("x", []byte(multiSite))
	if err := f.SetTunables(map[string]string{"notify.max": "7", "notify.repeat": "15m"}, []string{"localnet.min-domains"}); err != nil {
		t.Fatal(err)
	}
	_, cfg, ds := Load("x", f.Bytes(), Options{})
	if len(errorsOf(ds)) != 0 {
		t.Fatal(ds)
	}
	o := cfg.Tunables.Overrides()
	if o["notify.max"] != "7" || o["notify.repeat"] != "15m" || o["localnet.min-domains"] != "" {
		t.Fatalf("%v", o)
	}
	// File without $SET: a new line after the directives.
	f, _ = Parse("x", []byte(singleMachine))
	if err := f.SetTunables(map[string]string{"notify.max": "2"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.Split(string(f.Bytes()), "\n")[1], "$SET notify.max=2") {
		t.Fatalf("%s", f.Bytes())
	}
	// Removing the last key removes the line.
	if err := f.SetTunables(nil, []string{"notify.max"}); err != nil {
		t.Fatal(err)
	}
	if string(f.Bytes()) != singleMachine {
		t.Fatalf("%s", f.Bytes())
	}
}

func TestNextSerial(t *testing.T) {
	day := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		cur  uint32
		now  time.Time
		want uint32
		warn bool
	}{
		{2026102101, day, 2026102201, false},
		{2026102201, day, 2026102202, false},
		{2026102299, day, 2026102300, false}, // borrows the next day's prefix
		{5, day, 2026102201, false},
		{2026102101, day.AddDate(0, 0, 30), 2026102102, true}, // clock jumped
	}
	for _, c := range cases {
		got, warn, err := NextSerial(c.cur, c.now)
		if err != nil || got != c.want || (warn != "") != c.warn {
			t.Errorf("NextSerial(%d)=%d %q %v want %d", c.cur, got, warn, err, c.want)
		}
	}
	if _, _, err := NextSerial(MaxSerial, day); err == nil {
		t.Error("overflow")
	}
}

func TestCheckSerial(t *testing.T) {
	if CheckSerial(0, 0) == nil || CheckSerial(5, 6) == nil || CheckSerial(2000000, 1) == nil {
		t.Fatal("bad serials accepted")
	}
	if CheckSerial(7, 6) != nil || CheckSerial(1, 0) != nil {
		t.Fatal("good serials rejected")
	}
}

func TestDiffAndClassify(t *testing.T) {
	_, a, _ := load(t, multiSite)
	edited := strings.Replace(multiSite, `title="主数据库"`, `title="主库"`, 1)
	edited = strings.Replace(edited, "core-sw-ping     IN PING 10.1.1.1 loss=20%", "core-sw-ping     IN PING 10.1.1.2 loss=20%", 1)
	edited = strings.Replace(edited, "wan-ike       5m", "wan-ike       10m", 1)
	edited += "extra IN HTTP https://extra.example.com\n"
	_, b, _ := load(t, edited)
	cs := Diff(a, b)
	want := map[string]ChangeClass{"db-primary": ChangeMeta, "core-sw-ping": ChangeDefinition, "wan-ike": ChangeParams}
	got := map[string]ChangeClass{}
	added := false
	for _, c := range cs {
		if c.Kind == "changed" && c.Type == "MONITOR" {
			got[c.Record] = c.Class
		}
		added = added || (c.Kind == "added" && c.Record == "extra")
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: class %d want %d (%v)", k, got[k], v, cs)
		}
	}
	if !added || Summarize(cs).Added != 1 {
		t.Errorf("added: %v", cs)
	}
	if d := UnifiedDiff("a\nb\nc", "a\nx\nc"); !strings.Contains(d, "- b") || !strings.Contains(d, "+ x") {
		t.Errorf("%s", d)
	}
}

func TestRnameAndQuote(t *testing.T) {
	if rnameToEmail("first\\.last.example.com.") != "first.last@example.com" || rnameToEmail("ops@x.com") != "ops@x.com" {
		t.Fatal("rname")
	}
	if quote("a b") != `"a b"` || quote(`x"y`) != `"x\"y"` || quote("abc") != "abc" || quote("") != `""` {
		t.Fatal("quote")
	}
}

// Service checks are written by name and run as TCP checks that speak the
// service's protocol; containers are local checks.
func TestServiceTypes(t *testing.T) {
	src := `@ IN SOA t. admin.localhost. ( 2026102801 1h 1h 1h 1 )
cache IN REDIS 10.0.0.20 password=${secret:redis}
db    IN POSTGRES db.lan:6432 user=mon database=app
my    IN MYSQL [2001:db8::5]
api   IN GRPC api.lan:443 tls=yes service=api.v1
web   IN DOCKER nginx
`
	_, cfg, diags := load(t, src)
	for _, d := range diags {
		if d.Severity == SevError {
			t.Fatalf("%v", d)
		}
	}
	want := map[string][3]string{
		"cache": {"TCP", "10.0.0.20:6379", "redis"},
		"db":    {"TCP", "db.lan:6432", "postgres"},
		"my":    {"TCP", "[2001:db8::5]:3306", "mysql"},
		"api":   {"TCP", "api.lan:443", "grpc"},
		"web":   {"DOCKER", "nginx", ""},
	}
	for name, w := range want {
		m := cfg.FindMonitor(name)
		if m == nil || m.Type != w[0] || m.Target != w[1] || m.Opts["proto"] != w[2] {
			t.Errorf("%s: %+v", name, m)
		}
	}
	if m := cfg.FindMonitor("web"); m.Scope != "lan" || m.Written != "DOCKER" {
		t.Errorf("docker scope %q written %q", m.Scope, m.Written)
	}
	if m := cfg.FindMonitor("db"); m.Written != "POSTGRES" || m.Host() != "db.lan" {
		t.Errorf("postgres written %q host %q", m.Written, m.Host())
	}
	for _, bad := range []string{"x IN REDIS 10.0.0.1:99999", "x IN MYSQL user@db", "x IN GRPC http://api"} {
		_, _, diags := load(t, "@ IN SOA t. admin.localhost. ( 2026102801 1h 1h 1h 1 )\n"+bad+"\n")
		if !HasErrors(diags) {
			t.Errorf("accepted %q", bad)
		}
	}
}
