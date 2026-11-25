package config

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The examples of the syntax reference are complete files; each must load
// without errors, so the page cannot drift from the parser.
func TestSpecExamples(t *testing.T) {
	doc, err := os.ReadFile("../../docs/spec/monitors-conf.md")
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may turn the page's line ends into CRLF.
	doc = bytes.ReplaceAll(doc, []byte("\r\n"), []byte("\n"))
	blocks := regexp.MustCompile("(?s)```conf\n(.*?)```").FindAllSubmatch(doc, -1)
	if len(blocks) < 3 {
		t.Fatalf("found %d conf examples", len(blocks))
	}
	for i, b := range blocks {
		_, _, diags := Load("example", b[1], Options{})
		for _, d := range diags {
			if d.Severity == SevError {
				t.Errorf("example %d: %s: %s [%s]", i+1, d.Pos, d.Msg, d.Code)
			}
		}
	}
}

func TestVersionAndReservedNames(t *testing.T) {
	soa := "@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n"
	for _, c := range []struct {
		src, code string // code "" = no error
	}{
		{"$VERSION 1\n" + soa, ""},
		{soa + "$VERSION 1\n", ""},
		{"$VERSION 2\n" + soa, "unsupported_version"},
		{"$VERSION 1\n$VERSION 1\n" + soa, "duplicate_directive"},
		{"$QUORUM all\n" + soa, "soa_first"},
		{soa + "$TIMEZONE Asia/Shanghai\n", "needs_newer"},
		{soa + "ops IN SCHEDULE members=a,b\n", "needs_newer"},
		{soa + "ops IN PERSON phone=1\n", "needs_newer"},
		{soa + "$INCLUDE other.bind\n", "unsupported"},
		{soa + "x IN WHATEVER\n", "unknown_type"},
	} {
		_, _, diags := Load("f", []byte(c.src), Options{})
		var codes []string
		for _, d := range diags {
			if d.Severity == SevError {
				codes = append(codes, d.Code)
			}
		}
		got := strings.Join(codes, ",")
		if c.code == "" && got != "" || c.code != "" && !strings.Contains(","+got+",", ","+c.code+",") {
			t.Errorf("%q: errors %q, want %q", c.src, got, c.code)
		}
	}
	_, _, diags := Load("f", []byte(soa+"ops 30s IN PERIOD \"mon 09:00-18:00\"\n"), Options{})
	if len(diags) != 1 || diags[0].Code != "interval_ignored" || diags[0].Severity == SevError {
		t.Errorf("interval on a period: %v", diags)
	}
}

func specOf(r Record) RecordSpec {
	return RecordSpec{Name: r.Name, Interval: r.Interval, Type: r.Type, Target: r.Target, Opts: append([]Opt(nil), r.Opts...)}
}

// Writing a record back unchanged changes no byte; changing one value
// changes that value and nothing else.
func FuzzUpsertRoundTrip(f *testing.F) {
	for _, p := range []string{"testdata/single.bind", "testdata/multi.bind"} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b, "new value")
		}
	}
	f.Add([]byte(scopeSrc), "x")
	f.Add([]byte("@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\nweb  IN HTTP https://h/?a=b   title=\"A b\"  tags=x ; c\n"), "a=b")
	f.Fuzz(func(t *testing.T, src []byte, val string) {
		if !strings.Contains(string(src), "IN") || strings.ContainsAny(val, "\r\n") {
			return
		}
		file, diags := Parse("f", src)
		if HasErrors(diags) {
			return
		}
		var recs []Record
		for i := range file.Stmts {
			if r, ok := StmtRecord(&file.Stmts[i]); ok && r.Type != "SOA" && ValidName(r.Name) {
				recs = append(recs, r)
			}
		}
		for _, r := range recs {
			if r.TargetOK && r.Target == "" || dupKeys(r.Opts) != nil {
				continue
			}
			g, _ := Parse("f", src)
			if err := g.UpsertRecord(specOf(r)); err != nil {
				continue
			}
			if !bytes.Equal(g.Bytes(), src) {
				t.Fatalf("unchanged %s rewrote the file:\n%s\n---\n%s", r.Name, src, g.Bytes())
			}
			if len(r.Opts) == 0 {
				continue
			}
			spec := specOf(r)
			spec.Opts[len(spec.Opts)/2].Value = val
			if err := g.UpsertRecord(spec); err != nil {
				continue
			}
			got, ok := g.Record(r.Name)
			if !ok {
				t.Fatalf("%s lost after editing:\n%s", r.Name, g.Bytes())
			}
			if specString(specOf(got)) != specString(spec) {
				t.Fatalf("%s: got %+v, want %+v\n%s", r.Name, specOf(got), spec, g.Bytes())
			}
			for _, o := range recs {
				if o.Name == r.Name {
					continue
				}
				if og, ok := g.Record(o.Name); !ok || specString(specOf(og)) != specString(specOf(o)) {
					t.Fatalf("editing %s changed %s:\n%s", r.Name, o.Name, g.Bytes())
				}
			}
		}
	})
}

func specString(s RecordSpec) string {
	var b strings.Builder
	b.WriteString(s.Name + "|" + s.Interval + "|" + strings.ToUpper(s.Type) + "|" + s.Target)
	for _, o := range s.Opts {
		b.WriteString("|" + o.Key + "=" + o.Value)
	}
	return b.String()
}
