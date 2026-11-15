package probe

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestValidateQuery(t *testing.T) {
	for q, ok := range map[string]bool{
		"SELECT count(*) FROM orders":               true,
		"select 1":                                  true,
		"  SELECT 1;  ":                             true,
		"SELECT COUNT(*) FROM t WHERE a = 'x y'":    true,
		"":                                          false,
		"SELECT":                                    false,
		"INSERT INTO t VALUES (1)":                  false,
		"UPDATE t SET a = 1":                        false,
		"DELETE FROM t":                             false,
		"DROP TABLE t":                              false,
		"SHOW STATUS":                               false, // no number to take from it
		"WITH x AS (SELECT 1) SELECT * FROM x":      false,
		"SELECT 1; DROP TABLE t":                    false, // a second statement
		"SELECT 1; SELECT 2":                        false,
		"SELECT * INTO OUTFILE '/tmp/x' FROM t":     false,
		"SELECT * INTO newtable FROM t":             false,
		"SELECT count(*) FROM t FOR UPDATE":         false,
		"SELECT count(*) FROM t LOCK IN SHARE MODE": false,
		"SELECT count(*) FROM t for  no key update": false,
		"SELECT 1 /* x */; DELETE FROM t":           false,
		strings.Repeat("SELECT 1 ", 300):            false, // longer than the limit
		"SELECT\x001":                               false,
	} {
		if err := validateQuery(q); (err == nil) != ok {
			t.Errorf("%q: ok=%v err=%v", q, ok, err)
		}
	}
	if statement("  SELECT 1 ; ") != "SELECT 1" {
		t.Errorf("statement: %q", statement("  SELECT 1 ; "))
	}
}

func TestQueryNumber(t *testing.T) {
	o := opts{"query": "SELECT count(*) FROM orders"}
	r := queryNumber(Result{Vote: VoteOK, Detail: "PostgreSQL 16"}, o, " 42 ", false, 1)
	if r.Vote != VoteOK || r.Metrics["value"] != 42 || !strings.Contains(r.Detail, "= 42") {
		t.Fatalf("%+v", r)
	}
	for name, c := range map[string]struct {
		text string
		null bool
		rows int
		want string
	}{
		"no rows": {"", false, 0, "no rows"},
		"null":    {"", true, 1, "NULL"},
		"text":    {"abc", false, 1, "not a number"},
		"empty":   {"", false, 1, "not a number"},
	} {
		if r := queryNumber(Result{Vote: VoteOK}, o, c.text, c.null, c.rows); r.Vote != VoteFail || !strings.Contains(r.Detail, c.want) {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestSQLWireHelpers(t *testing.T) {
	// A PostgreSQL DataRow: two columns, the first is "42".
	row := binary.BigEndian.AppendUint16(nil, 2)
	row = binary.BigEndian.AppendUint32(row, 2)
	row = append(row, "42"...)
	row = binary.BigEndian.AppendUint32(row, 1)
	row = append(row, 'x')
	if s, null := pgFirstColumn(row); s != "42" || null {
		t.Errorf("first column %q %v", s, null)
	}
	nullRow := append(binary.BigEndian.AppendUint16(nil, 1), 0xff, 0xff, 0xff, 0xff)
	if _, null := pgFirstColumn(nullRow); !null {
		t.Error("a -1 length is NULL")
	}
	if _, null := pgFirstColumn([]byte{0, 1, 0, 0, 0, 99, 'a'}); !null { // longer than the message
		t.Error("a column longer than the row is refused")
	}
	for _, c := range []struct {
		in   []byte
		n    uint64
		rest int
		ok   bool
	}{
		{[]byte{5, 'a'}, 5, 1, true},
		{[]byte{0xfc, 0x01, 0x02, 'a'}, 0x0201, 1, true},
		{[]byte{0xfd, 1, 0, 0, 'a'}, 1, 1, true},
		{[]byte{0xfc, 1}, 0, 0, false}, // cut short
		{nil, 0, 0, false},
	} {
		n, rest, ok := myLenEnc(c.in)
		if ok != c.ok || (ok && (n != c.n || len(rest) != c.rest)) {
			t.Errorf("myLenEnc(%v) = %d %d %v", c.in, n, len(rest), ok)
		}
	}
}

func TestQueryOptions(t *testing.T) {
	spec := func(proto string, o map[string]string) Spec {
		o["proto"] = proto
		return Spec{Type: "TCP", Target: "127.0.0.1:5432", Opts: o}
	}
	for _, c := range []struct {
		proto string
		o     map[string]string
		ok    bool
	}{
		{"postgres", map[string]string{"user": "m", "query": "SELECT 1", "warn": "5", "crit": "9"}, true},
		{"mysql", map[string]string{"user": "m", "query": "SELECT 1", "crit": "<1"}, true},
		{"postgres", map[string]string{"user": "m", "query": "DROP TABLE t"}, false},
		{"postgres", map[string]string{"user": "m", "query": "SELECT 1; SELECT 2"}, false},
		{"postgres", map[string]string{"query": "SELECT 1"}, false},                            // no user to log in as
		{"postgres", map[string]string{"user": "m", "warn": "5"}, false},                       // nothing to judge
		{"postgres", map[string]string{"user": "m", "query": "SELECT 1", "field": "x"}, false}, // INFO is Redis's
		{"redis", map[string]string{"query": "SELECT 1"}, false},
		{"grpc", map[string]string{"warn": "5"}, false},
		{"postgres", map[string]string{"user": "m", "query": "SELECT 1", "crit": "5", "warn": "9"}, false},
	} {
		if err := Validate(spec(c.proto, c.o)); (err == nil) != c.ok {
			t.Errorf("%s %v: ok=%v err=%v", c.proto, c.o, c.ok, err)
		}
	}
}
