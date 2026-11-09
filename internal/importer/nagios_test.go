package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
)

const nagiosMain = "testdata/nagios/etc/nagios4/nagios.cfg"

// The fixture is a Debian-style layout: nagios.cfg names the plugin
// commands, conf.d and a resource file by absolute paths.
func TestNagiosTree(t *testing.T) {
	in, err := ReadNagiosFiles([]string{nagiosMain})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Missing) != 0 {
		t.Fatalf("missing: %v", in.Missing)
	}
	for _, f := range in.Files {
		if strings.Contains(f, "printer") {
			t.Fatal("a file nagios.cfg does not name was read")
		}
	}
	base := soaLine + "old-web1 IN TCP 10.0.1.11:22\n"
	_, existing, _ := config.Load("monitors.conf", []byte(base), config.Options{})
	res, err := NagiosInput(in, existing, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	noSecretsInText(t, res)
	text, cfg := apply(t, base, res)
	golden(t, "nagios.bind", text)
	golden(t, "nagios.txt", res.Report(ReportOptions{Width: 80, Config: "monitors.conf"}))
	golden(t, "nagios.md", res.Markdown(ReportOptions{Config: "monitors.conf"}))
	for _, w := range []int{40, 60, 100} {
		reportWidth(t, res.Report(ReportOptions{Width: w}), w)
	}

	for _, bad := range []string{"FAKEpass", "FAKE-resource-secret", "$USER1$", "/usr/lib/nagios", "printer", "'"} {
		if strings.Contains(res.Text, bad) {
			t.Errorf("the records contain %q", bad)
		}
	}
	// Quoted Debian arguments are split like the shell would.
	shop := cfg.FindMonitor("web1-https-shop")
	if shop == nil || shop.Target != "https://shop.example.com/cart" || shop.Opts["keyword"] != "Add to cart" || shop.Opts["cert-days"] != "21" {
		t.Fatalf("shop: %+v", shop)
	}
	// Hosts in nested groups keep their first group; services depend on
	// their host; the CRLF file is read.
	if w := cfg.FindMonitor("web1-http"); w == nil || w.Group != "web-servers" || w.Opts["parent"] != "web1" {
		t.Fatalf("web1-http: %+v", w)
	}
	if cfg.FindMonitor("crlf-host") == nil {
		t.Fatal("the CRLF file was not read")
	}
	// $USER3$ from resource.cfg becomes a secret.
	if a := cfg.FindMonitor("web2-api-token"); a == nil || !strings.Contains(a.Opts["header.authorization"], "${secret:nagios_user3}") {
		t.Fatalf("api token: %+v", a)
	}
	// Plugins are imported paused.
	for _, m := range cfg.Monitors {
		if m.Written == "EXEC" && !m.Disabled {
			t.Errorf("%s runs a plugin and must start paused", m.Name)
		}
	}
	// Passive checks wait for pushes.
	if p := cfg.FindMonitor("core-sw1-passive-trap"); p == nil || p.Type != "PUSH" {
		t.Fatalf("passive: %+v", p)
	}
	notes := strings.Join(res.Notes, "\n")
	for _, w := range []string{"carol", "ghost-host", "check_foo", "$_HOSTSITE$", "$USER2$", "serviceescalation", "pager number", "restart-app.sh", "holidays-excluded"} {
		if !strings.Contains(notes, w) {
			t.Errorf("notes lack %q:\n%s", w, notes)
		}
	}
	// Importing again adds nothing.
	again, err := NagiosInput(in, cfg, Options{Now: testNow, SecretExists: func(n string) bool { _, ok := res.Secrets[n]; return ok }})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Records) != 0 {
		t.Fatalf("re-import adds records:\n%s", again.Text)
	}
}

// A folder of object files works without nagios.cfg.
func TestNagiosDirectory(t *testing.T) {
	in, err := ReadNagiosFiles([]string{"testdata/nagios/etc/nagios4/conf.d", "testdata/nagios/etc/nagios4/objects/base.cfg", "testdata/nagios/etc/nagios-plugins/config"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NagiosInput(in, nil, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, soaLine, res)
	if res.Monitors < 20 {
		t.Fatalf("monitors %d", res.Monitors)
	}
	// Without resource.cfg $USER3$ cannot be filled in, and the report says why.
	if !strings.Contains(strings.Join(res.Notes, "\n"), "no resource file") {
		t.Errorf("notes:\n%s", strings.Join(res.Notes, "\n"))
	}
}

func TestNagiosSingleFile(t *testing.T) {
	data, err := os.ReadFile("testdata/nagios.cfg")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Nagios(data, nil, Options{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, soaLine, res)
}

func TestNagiosMessy(t *testing.T) {
	host := "define host {\n  host_name  %s\n  address 10.0.0.%d\n  check_command check-host-alive\n}\n"
	cmd := "define command {\n command_name check-host-alive\n command_line $USER1$/check_ping -H $HOSTADDRESS$\n}\n"
	cases := map[string]string{
		"crlf":        strings.ReplaceAll(cmd+fmt.Sprintf(host, "a", 1), "\n", "\r\n"),
		"tabs":        strings.ReplaceAll(cmd+fmt.Sprintf(host, "a", 1), "  ", "\t\t"),
		"comments":    "# top\n; also\n" + cmd + "define host { ; a comment\n host_name a ; trailing\n address 10.0.0.1 # not a comment\n}\n",
		"brace":       cmd + "define host{\n host_name a\n address 10.0.0.1 }\n",
		"unknown":     cmd + "define host {\n host_name a\n address 10.0.0.1\n frobnicate yes\n}\ndefine wibble {\n x y\n}\n",
		"unclosed":    cmd + fmt.Sprintf(host, "a", 1) + "define host {\n host_name broken\n" + fmt.Sprintf(host, "b", 2),
		"no-address":  cmd + "define host {\n host_name lonely\n}\n",
		"no-name":     cmd + "define host {\n address 10.0.0.9\n}\n" + fmt.Sprintf(host, "a", 1),
		"utf8":        cmd + "define host {\n host_name caf\xe9\n address 10.0.0.1\n alias Café ☕\n}\n",
		"bad-define":  cmd + "define {\n}\n" + fmt.Sprintf(host, "a", 1),
		"loop":        cmd + "define host {\n name t1\n use t2\n register 0\n}\ndefine host {\n name t2\n use t1\n register 0\n}\ndefine host {\n use t1\n host_name a\n address 10.0.0.1\n}\n",
		"empty-value": cmd + "define host {\n host_name a\n address\n}\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := Nagios([]byte(in), nil, Options{Now: testNow})
			if err != nil {
				t.Fatalf("%v", err)
			}
			apply(t, soaLine, res)
			if res.Monitors == 0 {
				t.Fatalf("nothing imported:\n%s", strings.Join(res.Notes, "\n"))
			}
		})
	}
	// A broken definition is reported with its line, the rest imported.
	res, _ := Nagios([]byte(cases["unclosed"]), nil, Options{})
	if notes := strings.Join(res.Notes, "\n"); !strings.Contains(notes, "line 10: define host is not closed") {
		t.Errorf("notes:\n%s", notes)
	}
	for _, s := range []string{"", "nothing here\n", "define host {\n host_name x\n", "\x00\x01\x02"} {
		if _, err := Nagios([]byte(s), nil, Options{}); err == nil {
			t.Errorf("%q: no error", s)
		}
	}
}

// A big configuration is read in reasonable time.
func TestNagiosHuge(t *testing.T) {
	var b strings.Builder
	b.WriteString("define command {\n command_name check-host-alive\n command_line $USER1$/check_ping -H $HOSTADDRESS$\n}\n")
	b.WriteString("define command {\n command_name check_tcp\n command_line $USER1$/check_tcp -H $HOSTADDRESS$ -p $ARG1$\n}\n")
	b.WriteString("define contact {\n contact_name ops\n email ops@example.com\n}\n")
	const hosts = 3000
	for i := 0; i < hosts; i++ {
		fmt.Fprintf(&b, "define host {\n host_name h%04d\n address 10.%d.%d.%d\n check_command check-host-alive\n contacts ops\n hostgroups g%d\n}\n", i, i/65536, i/256%256, i%256, i%20)
		fmt.Fprintf(&b, "define service {\n host_name h%04d\n service_description SSH\n check_command check_tcp!22\n contacts ops\n}\n", i)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "big.cfg")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	in, err := ReadNagiosFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NagiosInput(in, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Monitors != 2*hosts {
		t.Fatalf("monitors %d", res.Monitors)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("took %v", d)
	}
	apply(t, soaLine, res)
}

// Contacts are mailed the way Nagios mailed them: through this machine's
// sendmail command, or its SMTP port when it has none (pointed out).
func TestNagiosContactMail(t *testing.T) {
	cfg := "define contact {\n contact_name ops\n email ops@example.com\n}\n" +
		"define host {\n host_name web\n address 10.0.0.1\n contacts ops\n}\n"
	for _, have := range []bool{true, false} {
		haveSendmail = func() bool { return have }
		res, err := Nagios([]byte(cfg), nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		text, _ := apply(t, soaLine, res)
		want := "IN CHANNEL email via=sendmail to=ops@example.com"
		if !have {
			want = "IN CHANNEL email host=localhost port=25 tls=none from=unagikuma@localhost to=ops@example.com"
		}
		if !strings.Contains(text, want) {
			t.Errorf("sendmail %v: want %q in\n%s", have, want, text)
		}
		var flagged bool
		for _, it := range res.Items {
			flagged = flagged || it.Kind == "channel" && strings.Contains(it.Why, "no sendmail command")
		}
		if flagged == have {
			t.Errorf("sendmail %v: flagged %v", have, flagged)
		}
	}
	haveSendmail = func() bool { return true }
}
