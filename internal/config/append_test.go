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

// A monitor added with First heads its section; the others keep their order.
func TestUpsertFirst(t *testing.T) {
	src := "@ IN SOA a. b. ( 2026010101 1m 15s 5s 3 )\n" +
		"; the shop\n" +
		"web  1m IN HTTP https://example.com/\n" +
		"db   1m IN TCP db:5432\n"
	f, diags := Parse("t", []byte(src))
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	for _, n := range []string{"lan-gateway", "internet-gateway"} {
		if err := f.UpsertRecord(RecordSpec{Name: n, Type: "PING", Interval: "1m", Target: "10.0.0.1", First: true}); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, r := range f.Records() {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, " "); got != "internet-gateway lan-gateway web db" {
		t.Fatalf("order %q\n%s", got, f.Bytes())
	}
	if !strings.Contains(string(f.Bytes()), "; the shop\ninternet-gateway") {
		t.Fatalf("comment moved:\n%s", f.Bytes())
	}
}

// A monitor put first does not land under a $DEFAULT written before the
// first monitor: it would take options meant for the others (a header on
// a PING, a secret).
func TestUpsertFirstAboveDefault(t *testing.T) {
	src := "$FILE revision=1 master=a interval=1m retry=15s timeout=5s attempts=3\n" +
		"$DEFAULT header.Authorization=\"Bearer x\"\n" +
		"api 15s IN HTTP http://127.0.0.1/\n" +
		"$DEFAULT\n"
	f, diags := Parse("t", []byte(src))
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	if err := f.UpsertRecord(RecordSpec{Name: "lan-gateway", Type: "PING", Interval: "1m", Target: "10.0.0.1", First: true}); err != nil {
		t.Fatal(err)
	}
	_, cfg, diags := Load("t", f.Bytes(), Options{})
	if HasErrors(diags) {
		t.Fatalf("%v\n%s", diags, f.Bytes())
	}
	m := cfg.FindMonitor("lan-gateway")
	if m == nil || m.Opts["header.authorization"] != "" || m.Opts["header.Authorization"] != "" {
		t.Fatalf("the gateway took the $DEFAULT:\n%s", f.Bytes())
	}
	if a := cfg.FindMonitor("api"); a == nil || len(a.Opts) == 0 {
		t.Fatalf("api lost its $DEFAULT:\n%s", f.Bytes())
	}
}
