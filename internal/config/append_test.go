package config

import (
	"strings"
	"testing"
)

func TestAppendBlock(t *testing.T) {
	src := "@ IN SOA a. b. ( 2026010101 1m 15s 5s 3 )\r\nwww IN HTTP https://example.com/\r\n$SCOPE region:north\r\nx IN PING 10.0.0.1\r\n\r\n\r\n"
	f, diags := Parse("t", []byte(src))
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	if got := f.EndArea(); got != "region:north" {
		t.Fatalf("EndArea = %q", got)
	}
	if err := f.AppendBlock("; --- imported ---\n$SCOPE *\ngw IN PING 10.0.0.2\n"); err != nil {
		t.Fatal(err)
	}
	want := "@ IN SOA a. b. ( 2026010101 1m 15s 5s 3 )\r\nwww IN HTTP https://example.com/\r\n$SCOPE region:north\r\nx IN PING 10.0.0.1\r\n\r\n; --- imported ---\r\n$SCOPE *\r\ngw IN PING 10.0.0.2\r\n"
	if string(f.Bytes()) != want {
		t.Fatalf("got %q", f.Bytes())
	}
	if f.EndArea() != "" {
		t.Fatal("EndArea after $SCOPE *")
	}
	if err := f.AppendBlock("bad IN PING ( 10.0.0.3"); err == nil || !strings.Contains(string(f.Bytes()), "gw IN PING") {
		t.Fatalf("invalid block accepted or file changed: %v", err)
	}
}

// A record added to a group goes after its group's records and is
// aligned like them, not like the last record of the file.
func TestUpsertAlignsWithGroup(t *testing.T) {
	src := "@ IN SOA a. b. ( 2026010101 1m 15s 5s 3 )\n" +
		"gw   1m IN PING 10.0.0.1 group=net\n" +
		"\n" +
		"a-very-long-record-name  1m IN HTTP     https://example.com/\n"
	f, diags := Parse("t", []byte(src))
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	if err := f.UpsertRecord(RecordSpec{Name: "sw", Type: "PING", Interval: "1m", Target: "10.0.0.2", Opts: []Opt{{Key: "group", Value: "net"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.Bytes()), "gw   1m IN PING 10.0.0.1 group=net\nsw   1m IN PING 10.0.0.2 group=net\n") {
		t.Fatalf("got:\n%s", f.Bytes())
	}
}
