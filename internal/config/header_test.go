package config

import (
	"strings"
	"testing"
	"time"
)

// The file header names what the older SOA record held by position; only
// the revision is required. A file with an SOA record reads as before.
func TestFileHeader(t *testing.T) {
	const body = "c IN CHANNEL log\ndefault IN POLICY chain=c\nweb IN HTTP https://example.com/\n"
	_, cfg, ds := load(t, "; my monitors\n$FILE revision=7\n"+body)
	if e := errorsOf(ds); len(e) > 0 {
		t.Fatal(e)
	}
	if cfg.Serial != 7 || cfg.MName != "unagikuma" || cfg.Defaults != headerDefaults {
		t.Fatalf("defaults: serial %d master %q %+v", cfg.Serial, cfg.MName, cfg.Defaults)
	}
	if m := cfg.FindMonitor("web"); m.Interval != time.Minute || m.Attempts != 3 {
		t.Fatalf("monitor defaults: %+v", m)
	}
	_, cfg, ds = load(t, "$FILE revision=2026110401 master=bj admin=ops@example.com interval=30s retry=10s timeout=4s attempts=2\nbj IN NODE\n"+body)
	if e := errorsOf(ds); len(e) > 0 {
		t.Fatal(e)
	}
	want := Defaults{Interval: 30 * time.Second, Retry: 10 * time.Second, Timeout: 4 * time.Second, Attempts: 2}
	if cfg.Serial != 2026110401 || cfg.MName != "bj" || cfg.RName != "ops@example.com" || cfg.Defaults != want {
		t.Fatalf("%d %q %q %+v", cfg.Serial, cfg.MName, cfg.RName, cfg.Defaults)
	}
	// The same file with an SOA record.
	_, old, ds := load(t, "@ IN SOA bj. ops.example.com. ( 2026110401 30s 10s 4s 2 )\nbj IN NODE\n"+body)
	if e := errorsOf(ds); len(e) > 0 || old.Serial != cfg.Serial || old.MName != cfg.MName || old.RName != cfg.RName || old.Defaults != cfg.Defaults {
		t.Fatalf("SOA reads differently: %+v %v", old.Defaults, e)
	}

	for src, code := range map[string]string{
		"$FILE master=x\n" + body:                                       "invalid_serial",
		"$FILE revision=0\n" + body:                                     "invalid_serial",
		"$FILE revision=1 interval=soon\n" + body:                       "invalid_duration",
		"$FILE revision=1 attempts=0\n" + body:                          "invalid_value",
		"$FILE revision=1 colour=red\n" + body:                          "unknown_option",
		"$FILE revision=1 revision=2\n" + body:                          "duplicate_option",
		"$FILE revision=1 1m\n" + body:                                  "syntax",
		"$FILE revision=1\n$FILE revision=2\n" + body:                   "duplicate_soa",
		"$FILE revision=1\n@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n" + body: "duplicate_soa",
		body + "$FILE revision=1\n":                                     "soa_first",
		"$QUORUM majority\n$FILE revision=1\n" + body:                   "soa_first",
		body: "soa_first",
	} {
		_, _, ds := load(t, src)
		found := false
		for _, d := range ds {
			found = found || d.Code == code && d.Severity == SevError
		}
		if !found {
			t.Errorf("%q: want error %s, got %v", src, code, ds)
		}
	}
	_, _, ds = load(t, "$VERSION 1\n$FILE revision=1\n$QUORUM majority\n"+body)
	if e := errorsOf(ds); len(e) > 0 {
		t.Fatalf("$VERSION before the header: %v", e)
	}
	_, _, ds = load(t, "$FILE revision=1 interval=5s timeout=5s\n"+body)
	if !strings.Contains(diagCodes(ds), "timeout_ge_interval") {
		t.Errorf("timeout not shorter than interval: %v", ds)
	}
}

// Saving from the web UI raises the revision on the $FILE line in place.
func TestFileHeaderRevision(t *testing.T) {
	for _, src := range []string{
		"$FILE revision=7 interval=1m ; keep\nc IN CHANNEL log\n",
		"$FILE interval=1m Revision=\"7\"\r\nc IN CHANNEL log\r\n",
	} {
		f, ds := Parse("x", []byte(src))
		if len(ds) > 0 {
			t.Fatal(ds)
		}
		if n, err := f.Serial(); err != nil || n != 7 {
			t.Fatalf("%q: %d %v", src, n, err)
		}
		if err := f.SetSerial(2026110402); err != nil {
			t.Fatal(err)
		}
		if n, _ := f.Serial(); n != 2026110402 {
			t.Fatalf("%q: after set %d", src, n)
		}
		out := string(f.Bytes())
		if changedLines(src, out) != 1 || !strings.Contains(out, "=2026110402") || strings.Contains(src, "; keep") && !strings.Contains(out, "; keep") {
			t.Fatalf("%q -> %q", src, out)
		}
	}
	f, _ := Parse("x", []byte("$FILE interval=1m\n"))
	if _, err := f.Serial(); err == nil {
		t.Fatal("a header without revision has a serial")
	}
	// New tunables go after the header.
	f, _ = Parse("x", []byte("$FILE revision=1\nc IN CHANNEL log\n"))
	if err := f.SetTunables(map[string]string{"notify.max": "2"}, nil); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(string(f.Bytes()), "\n"); lines[1] != "$SET notify.max=2" {
		t.Fatalf("%q", f.Bytes())
	}
	// fmt leaves the header alone.
	src := "$FILE   revision=1    interval=1m\nweb IN HTTP https://example.com/\n"
	if out := string(Format([]byte(src))); !strings.HasPrefix(out, "$FILE   revision=1    interval=1m\n") {
		t.Fatalf("fmt: %q", out)
	}
}

func diagCodes(ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString(d.Code + " ")
	}
	return b.String()
}

func TestSetMaster(t *testing.T) {
	const body = "c IN CHANNEL log\ndefault IN POLICY chain=c\nweb IN HTTP https://example.com/\n"
	f, ds := Parse("t.bind", []byte("$FILE revision=1 master=old\nb IN NODE\n"+body))
	if HasErrors(ds) {
		t.Fatal(ds)
	}
	if err := f.SetMaster("b"); err != nil {
		t.Fatal(err)
	}
	_, cfg, ds := Load("t.bind", f.Bytes(), Options{})
	if e := errorsOf(ds); len(e) > 0 || cfg.MName != "b" {
		t.Fatalf("master %q diags %v", cfg.MName, e)
	}
	f, ds = Parse("t.bind", []byte("$FILE revision=2\nb IN NODE\n"+body))
	if HasErrors(ds) {
		t.Fatal(ds)
	}
	if err := f.SetMaster("b"); err != nil {
		t.Fatal(err)
	}
	_, cfg, ds = Load("t.bind", f.Bytes(), Options{})
	if e := errorsOf(ds); len(e) > 0 || cfg.MName != "b" {
		t.Fatalf("added master %q diags %v text %s", cfg.MName, e, f.Bytes())
	}
	f, ds = Parse("t.bind", []byte("@ IN SOA old. a.b. ( 3 1m 15s 5s 3 )\nold IN NODE\n"+body))
	if HasErrors(ds) {
		t.Fatal(ds)
	}
	if err := f.SetMaster("b"); err != nil {
		t.Fatal(err)
	}
	_, cfg, ds = Load("t.bind", f.Bytes(), Options{})
	if e := errorsOf(ds); len(e) > 0 || cfg.MName != "b" {
		t.Fatalf("SOA master %q diags %v text %s", cfg.MName, e, f.Bytes())
	}
}
