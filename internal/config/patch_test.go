package config

import (
	"strings"
	"testing"
)

const patchSrc = `@ IN SOA m. a.b. ( 2026102901 1m 15s 5s 3 )
; web servers
web-1     1m IN HTTP  "https://a.example.com/x?y=1" keyword="Sign in" expect=200 tags=env:prod ; front
web-2        IN HTTP  https://b.example.com/ title=B   degraded=800ms
db (
    IN TCP 10.0.0.5:5432
    title="Primary DB"
    attempts=3
)
`

func patched(t *testing.T, spec RecordSpec) (string, int) {
	t.Helper()
	f, diags := Parse("x", []byte(patchSrc))
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	if err := f.UpsertRecord(spec); err != nil {
		t.Fatal(err)
	}
	out := string(f.Bytes())
	return out, changedLines(patchSrc, out)
}

func TestPatchKeepsOrderQuotesAndColumns(t *testing.T) {
	// One value changed: one line, everything else as written.
	out, n := patched(t, RecordSpec{Name: "web-1", Interval: "1m", Type: "HTTP", Target: "https://a.example.com/x?y=1",
		Opts: []Opt{{Key: "tags", Value: "env:prod"}, {Key: "keyword", Value: "Sign out"}, {Key: "expect", Value: "200"}}})
	want := `web-1     1m IN HTTP  "https://a.example.com/x?y=1" keyword="Sign out" expect=200 tags=env:prod ; front`
	if n != 1 || !strings.Contains(out, want) {
		t.Fatalf("changed %d lines:\n%s", n, out)
	}
	// Removed and added options.
	out, _ = patched(t, RecordSpec{Name: "web-2", Type: "HTTP", Target: "https://b.example.com/",
		Opts: []Opt{{Key: "title", Value: "Shop B"}, {Key: "cert-days", Value: "14"}}})
	if !strings.Contains(out, `web-2        IN HTTP  https://b.example.com/ title="Shop B" cert-days=14`+"\n") {
		t.Fatalf("remove/add:\n%s", out)
	}
	// A longer name keeps the IN column while there is room.
	out, _ = patched(t, RecordSpec{Name: "web-2", Type: "HTTP", Target: "https://b.example.com/",
		Opts: []Opt{{Key: "title", Value: "B"}, {Key: "degraded", Value: "800ms"}}})
	if !strings.Contains(out, "web-2        IN HTTP  https://b.example.com/ title=B   degraded=800ms") {
		t.Fatalf("unchanged record changed:\n%s", out)
	}
	// Multi-line records stay multi-line.
	out, n = patched(t, RecordSpec{Name: "db", Type: "TCP", Target: "10.0.0.5:5432",
		Opts: []Opt{{Key: "title", Value: "Primary DB"}, {Key: "attempts", Value: "5"}}})
	if n != 1 || !strings.Contains(out, "    attempts=5\n") {
		t.Fatalf("multi-line (%d lines changed):\n%s", n, out)
	}
	// Unquoted URL targets with a query are targets, not options.
	f, diags := Parse("x", []byte("@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\napi IN HTTP https://h.example.com/v1/health?token=abc&a=b keyword=ok\n"))
	r, ok := StmtRecord(&f.Stmts[1])
	if HasErrors(diags) || !ok || r.Target != "https://h.example.com/v1/health?token=abc&a=b" || len(r.Opts) != 1 {
		t.Fatalf("%v %+v", diags, r)
	}
}

func TestInsertPlacesAndAligns(t *testing.T) {
	src := `@ IN SOA m. a.b. ( 2026102901 1m 15s 5s 3 )
gw       IN PING 10.0.0.1          group=net
sw       IN PING 10.0.0.2          group=net
web      IN HTTP https://a.example group=apps
public   IN STATUSPAGE "Status" select="group:apps"
`
	f, _ := Parse("x", []byte(src))
	if err := f.UpsertRecord(RecordSpec{Name: "fw", Type: "PING", Target: "10.0.0.3", Opts: []Opt{{Key: "group", Value: "net"}}}); err != nil {
		t.Fatal(err)
	}
	out := string(f.Bytes())
	if !strings.Contains(out, "sw       IN PING 10.0.0.2          group=net\nfw       IN PING 10.0.0.3 group=net\nweb") {
		t.Fatalf("group placement or alignment:\n%s", out)
	}
	if err := f.UpsertRecord(RecordSpec{Name: "api", Type: "HTTP", Target: "https://api.example"}); err != nil {
		t.Fatal(err)
	}
	out = string(f.Bytes())
	if strings.Index(out, "api ") > strings.Index(out, "public ") {
		t.Fatalf("inserted after the status page:\n%s", out)
	}
	// Into a file with no monitors yet: before the status page.
	f, _ = Parse("x", []byte("@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\npublic IN STATUSPAGE \"S\"\n"))
	f.UpsertRecord(RecordSpec{Name: "a", Type: "PING", Target: "10.0.0.9"})
	if out := string(f.Bytes()); strings.Index(out, "a IN PING") > strings.Index(out, "public") {
		t.Fatalf("first monitor after the status page:\n%s", out)
	}
}

func TestFormatAlignsAndIsIdempotent(t *testing.T) {
	src := "@ IN SOA m. a.b. ( 2026102901 1m 15s 5s 3 )\n" +
		"; monitors\n" +
		"web 1m IN HTTP \"https://a.example\" title=\"Web\"   KEYWORD=ok ; front\n" +
		"database   IN TCP 10.0.0.5:5432\n" +
		"\n" +
		"x IN PING 10.0.0.9\n"
	out := string(Format([]byte(src)))
	want := "web      1m IN HTTP https://a.example title=Web keyword=ok ; front\n" +
		"database    IN TCP  10.0.0.5:5432\n"
	if !strings.Contains(out, want) || !strings.Contains(out, "\nx IN PING 10.0.0.9\n") || !strings.Contains(out, "; monitors\n") {
		t.Fatalf("format:\n%s", out)
	}
	if again := string(Format([]byte(out))); again != out {
		t.Fatalf("not idempotent:\n%s\n---\n%s", out, again)
	}
	// Same meaning.
	_, a, _ := Load("x", []byte(src), Options{})
	_, b, _ := Load("x", []byte(out), Options{})
	if len(a.Monitors) != len(b.Monitors) || a.Monitors[0].DefHash != b.Monitors[0].DefHash || a.Monitors[0].ParamHash != b.Monitors[0].ParamHash {
		t.Fatal("format changed the meaning")
	}
	for _, s := range []string{singleMachine, multiSite} {
		once := Format([]byte(s))
		if string(Format(once)) != string(once) {
			t.Fatal("example not idempotent")
		}
		_, c1, d1 := Load("x", []byte(s), Options{})
		_, c2, d2 := Load("x", once, Options{})
		if HasErrors(d1) != HasErrors(d2) || len(c1.Monitors) != len(c2.Monitors) {
			t.Fatal("example meaning changed")
		}
		for i := range c1.Monitors {
			if c1.Monitors[i].DefHash != c2.Monitors[i].DefHash || c1.Monitors[i].ParamHash != c2.Monitors[i].ParamHash || c1.Monitors[i].MetaHash != c2.Monitors[i].MetaHash {
				t.Fatalf("%s changed", c1.Monitors[i].Name)
			}
		}
	}
}
