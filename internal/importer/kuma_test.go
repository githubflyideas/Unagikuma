package importer

import (
	"os"
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
)

func TestKumaJSON(t *testing.T) {
	data, err := os.ReadFile("testdata/kuma.json")
	if err != nil {
		t.Fatal(err)
	}
	base := soaLine + "web IN PING 10.0.0.1\n"
	_, existing, _ := config.Load("t", []byte(base), config.Options{})
	res, err := Kuma(data, existing, Options{File: "kuma.json", Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	noSecretsInText(t, res)
	_, cfg := apply(t, base, res)
	if res.Monitors != 10 || res.Channels != 2 {
		t.Fatalf("monitors %d channels %d", res.Monitors, res.Channels)
	}
	site := cfg.FindMonitor("www.example.com") // 官网 has no ASCII name: named after its host
	if site == nil || site.Title != "官网" || site.Group != "Core" || site.Opts["keyword"] != "Welcome" || site.Attempts != 3 || site.Opts["cert-days"] != "62" {
		t.Fatalf("keyword monitor: %+v", site)
	}
	if p := cfg.FindPolicy(site.Policy); p == nil || strings.Join(p.Chain, ",") != "feishu,tg-ops" || p.Name != "notify-feishu-tg-ops" {
		t.Fatalf("policy: %+v", p)
	}
	if cfg.FindMonitor("postgres").Policy != site.Policy {
		t.Fatal("monitors with the same channels share a policy")
	}
	api := cfg.FindMonitor("api-health")
	if api.Opts["json-path"] != "$.status" || api.Opts["header.x-env"] != "prod" || !strings.HasPrefix(api.Opts["header.authorization"], "${secret:") {
		t.Fatalf("json monitor: %+v", api.Opts)
	}
	if cfg.FindMonitor("gateway") != nil {
		t.Fatal("an upside-down monitor must not be imported as a normal one")
	}
	if dns := cfg.FindMonitor("resolver"); dns.Type != "UDP" || dns.Opts["query"] != "corp.example.com" || dns.Opts["qtype"] != "MX" {
		t.Fatalf("dns: %+v", dns)
	}
	if w := cfg.FindMonitor("web-2"); w == nil || w.Title != "web" || w.Opts["method"] != "POST" || w.Opts["insecure"] != "yes" {
		t.Fatalf("renamed http monitor: %+v", w)
	}
	if r := cfg.FindMonitor("redis"); r == nil || r.Written != "REDIS" || !strings.HasPrefix(r.Opts["password"], "${secret:") {
		t.Fatalf("redis: %+v", r)
	}
	if res.PushTokens["backup-job"] != "abc" {
		t.Fatalf("push token: %v", res.PushTokens)
	}
	// Everything not imported as it was is in the report, with a reason.
	for _, it := range res.Items {
		if it.Status == StatusLeftOut && (it.Why == "" || it.Todo == "") {
			t.Errorf("%s %s: left out without a reason or what to do", it.Kind, it.Source)
		}
	}
	notes := strings.Join(res.Notes, "\n")
	for _, want := range []string{"MongoDB", "upside-down", "Mail (Email), Pager (PagerDuty)", "health check"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if _, err := Kuma([]byte(`{"foo":1}`), nil, Options{}); err == nil {
		t.Fatal("not a backup accepted")
	}
}

// The fixtures are real Uptime Kuma 1.23 and 2.0 data (database and JSON
// backup), with made-up names, addresses and credentials.
var kumaFixtures = []struct{ file, name string }{
	{"testdata/kuma-v1.db", "kuma-v1-db"},
	{"testdata/kuma-v1-backup.json", "kuma-v1-json"},
	{"testdata/kuma-v2.db", "kuma-v2-db"},
}

func TestKumaFixtures(t *testing.T) {
	for _, fx := range kumaFixtures {
		t.Run(fx.name, func(t *testing.T) {
			data, err := os.ReadFile(fx.file)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Kuma(data, nil, Options{File: "kuma", Now: testNow})
			if err != nil {
				t.Fatal(err)
			}
			noSecretsInText(t, res)
			text, cfg := apply(t, soaLine, res)
			golden(t, fx.name+".bind", text)
			golden(t, fx.name+".txt", res.Report(ReportOptions{Width: 80, Config: "monitors.conf"}))
			golden(t, fx.name+".md", res.Markdown(ReportOptions{Config: "monitors.conf"}))
			for _, w := range []int{40, 60, 100} {
				reportWidth(t, res.Report(ReportOptions{Width: w}), w)
			}
			// Groups keep their nesting, names their original spelling.
			if m := cfg.FindMonitor("api-health-json"); m == nil || m.Group != "Production / API" || m.Title != "API health (JSON)" {
				t.Fatalf("nested group: %+v", m)
			}
			if m := cfg.FindMonitor("www.example.cn"); m == nil || m.Title != "官网首页" {
				t.Fatalf("chinese name: %+v", m)
			}
			for _, m := range cfg.Monitors {
				if len(m.Name) > 40 {
					t.Errorf("name %s is too long", m.Name)
				}
			}
			if !strings.HasSuffix(fx.file, ".json") {
				if len(cfg.StatusPages) != 1 || cfg.StatusPages[0].Name != "public" || len(cfg.Maints) != 1 {
					t.Fatalf("status page or maintenance missing")
				}
			}
			// Importing again adds nothing.
			again, err := Kuma(data, cfg, Options{Now: testNow, SecretExists: func(n string) bool { _, ok := res.Secrets[n]; return ok },
				UserExists: func(string) bool { return true }})
			if err != nil {
				t.Fatal(err)
			}
			if len(again.Records) != 0 || again.count("", StatusImported, StatusAttention) != 0 {
				t.Fatalf("re-import adds %d records:\n%s", len(again.Records), again.Text)
			}
			if again.count("monitor", StatusSkipped) != res.Monitors {
				t.Fatalf("re-import skipped %d of %d monitors", again.count("monitor", StatusSkipped), res.Monitors)
			}
		})
	}
}

func TestKumaOddInput(t *testing.T) {
	for _, in := range []string{"", "{}", "[]", "null", `{"monitorList":null}`, `{"monitorList":[{}]}`, `{"monitorList":[{"type":"http"}]}`,
		`{"monitorList":[{"id":1,"type":"group","name":"g","parent":1}]}`, // a group that is its own parent
		`{"monitorList":[{"id":1,"type":"http","name":"x","url":"http://x","notificationIDList":{"9":true}}]}`,
		"SQLite format 3\x00garbage", "\xff\xfe"} {
		res, err := Kuma([]byte(in), nil, Options{})
		if err == nil {
			apply(t, soaLine, res)
		}
	}
	// A truncated database is refused with a plain message.
	data, _ := os.ReadFile("testdata/kuma-v1.db")
	if _, err := Kuma(data[:len(data)/3], nil, Options{}); err == nil || strings.Contains(err.Error(), "panic") {
		t.Fatalf("truncated database: %v", err)
	}
}
