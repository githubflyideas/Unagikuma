package importer

import (
	goflag "flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/notify"
)

var update = goflag.Bool("update", false, "rewrite the golden files in testdata/golden")

// testNow fixes the time in headers and reports.
var testNow = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

const soaLine = "@ IN SOA master. me.example.com. ( 2026102101 1m 15s 5s 3 )\n"

// golden compares got with testdata/golden/name (or rewrites it with
// -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if string(want) != got {
		wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
		for i := 0; i < len(wl) || i < len(gl); i++ {
			var w, g string
			if i < len(wl) {
				w = wl[i]
			}
			if i < len(gl) {
				g = gl[i]
			}
			if w != g {
				t.Fatalf("%s differs at line %d:\nwant %q\ngot  %q\n(run go test -update if the change is intended)", name, i+1, w, g)
			}
		}
	}
}

// apply appends an import to a configuration the way the command does,
// checks that the result is valid (channels included) and returns the
// new file and configuration.
func apply(t *testing.T, base string, res *Result) (string, *config.Config) {
	t.Helper()
	f, _, diags := config.Load("monitors.conf", []byte(base), config.Options{})
	if config.HasErrors(diags) {
		t.Fatalf("base: %v", diags)
	}
	if len(res.Records) > 0 {
		if err := f.AppendBlock(res.Render(f.EndArea())); err != nil {
			t.Fatalf("append: %v\n%s", err, res.Render(f.EndArea()))
		}
	}
	text := string(f.Bytes())
	known := func(name string) bool {
		_, ok := res.Secrets[name]
		return ok || strings.Contains(base, "${secret:"+name+"}")
	}
	_, cfg, diags := config.Load("monitors.conf", []byte(text), config.Options{SecretKnown: known, ValidateChannel: notify.ValidateChannel})
	if config.HasErrors(diags) {
		t.Fatalf("the imported records do not validate: %v\n%s", diags, text)
	}
	// What is written is what `unagikuma fmt` would write.
	if formatted := string(config.Format([]byte(text))); formatted != text {
		t.Errorf("the generated block is not formatted:\n%s", text)
	}
	return text, cfg
}

// noSecretsInText checks that no credential is written in clear.
func noSecretsInText(t *testing.T, res *Result) {
	t.Helper()
	for name, v := range res.Secrets {
		if len(v) >= 4 && (strings.Contains(res.Text, "="+v) || strings.Contains(res.Text, `"`+v) || strings.Contains(res.Text, ":"+v)) {
			t.Errorf("secret %s is written in clear", name)
		}
	}
}

// reportWidth checks that a terminal report fits its width (a single
// word longer than the width may overflow).
func reportWidth(t *testing.T, report string, width int) {
	t.Helper()
	for i, l := range strings.Split(report, "\n") {
		if n := len([]rune(l)); n > width && strings.Contains(strings.TrimSpace(l), " ") {
			t.Errorf("line %d is %d columns wide (max %d): %q", i+1, n, width, l)
		}
	}
}

// The golden files describe an import on a machine with a sendmail
// command, as a Nagios machine has; tests do not depend on this one.
func init() { haveSendmail = func() bool { return true } }
