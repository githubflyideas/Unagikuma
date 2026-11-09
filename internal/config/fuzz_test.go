package config

import (
	"bytes"
	"os"
	"testing"
)

func FuzzConfig(f *testing.F) {
	for _, p := range []string{"testdata/single.bind", "testdata/multi.bind"} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(scopeSrc))
	f.Add([]byte(selectSrc))
	f.Fuzz(func(t *testing.T, src []byte) {
		file, _ := Parse("f", src)
		if !bytes.Equal(file.Bytes(), src) {
			t.Fatal("parsing must keep the file byte for byte")
		}
		_, cfg, _ := Load("f", src, Options{})
		if cfg == nil {
			t.Fatal("Load returned no model")
		}
		// Editing never panics, whatever the file looks like.
		file.UpsertRecord(RecordSpec{Name: "fz", Type: "PING", Target: "10.0.0.1", Area: "region:x"})
		file.SetSerial(1)
	})
}

func FuzzSelector(f *testing.F) {
	f.Add("tag:site:bj -type:tcp cidr:10.0.0.0/8 name:/^b/")
	f.Add(`title:"web site" port:443,80`)
	_, cfg, _ := Load("f", []byte(selectSrc), Options{})
	f.Fuzz(func(t *testing.T, q string) {
		sel, err := ParseSelector(q)
		if err != nil {
			return
		}
		for _, m := range cfg.Monitors {
			sel.Match(m)
		}
	})
}

// Whatever a record is given through the API, writing it into the file
// and reading the file again gives back exactly those values.
func FuzzWriteBack(f *testing.F) {
	f.Add("http://192.168.1.10/health?a=b&c=d", "Web (main); \"quoted\"", "x=y")
	f.Add("https://例子.cn/路径", `back\slash`, "")
	f.Add("http://h/;comment", "a(b)c", "k=v=w")
	base := []byte("@ IN SOA m. a.b. ( 2026102801 1m 15s 5s 3 )\n")
	f.Fuzz(func(t *testing.T, target, title, keyword string) {
		spec := RecordSpec{Name: "m", Type: "HTTP", Target: target, Opts: []Opt{{Key: "title", Value: title}, {Key: "keyword", Value: keyword}}}
		file, _ := Parse("f", base)
		if err := file.UpsertRecord(spec); err != nil {
			return // refused values never reach the file
		}
		again, _ := Parse("f", file.Bytes())
		r, ok := again.Record("m")
		if !ok {
			t.Fatalf("record lost:\n%s", file.Bytes())
		}
		if r.Target != target {
			t.Fatalf("target %q read back as %q:\n%s", target, r.Target, file.Bytes())
		}
		got := map[string]string{}
		for _, o := range r.Opts {
			got[o.Key] = o.Value
		}
		for _, o := range spec.Opts {
			if o.Value != "" && got[o.Key] != o.Value {
				t.Fatalf("%s %q read back as %q:\n%s", o.Key, o.Value, got[o.Key], file.Bytes())
			}
		}
	})
}
