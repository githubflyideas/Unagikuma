package importer

import (
	"os"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
)

func FuzzKuma(f *testing.F) {
	if b, err := os.ReadFile("testdata/kuma.json"); err == nil {
		f.Add(b)
	}
	f.Add([]byte(`{"monitorList":[{"type":"http","name":"","url":"x"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := Kuma(data, nil, Options{})
		if err != nil {
			return
		}
		// Whatever comes in, what goes out parses as records.
		file, diags := config.Parse("f", []byte("@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n"+res.Text))
		for _, d := range diags {
			if d.Severity == config.SevError {
				t.Fatalf("generated records do not parse: %v\n%s", d, res.Text)
			}
		}
		_ = file
	})
}

func FuzzNagios(f *testing.F) {
	if b, err := os.ReadFile("testdata/nagios.cfg"); err == nil {
		f.Add(b)
	}
	f.Add([]byte("define host {\n host_name a b\n address \"x y\n check_command c!$ARG1$\n}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := Nagios(data, nil, Options{})
		if err != nil {
			return
		}
		_, diags := config.Parse("f", []byte("@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\n"+res.Text))
		for _, d := range diags {
			if d.Severity == config.SevError {
				t.Fatalf("generated records do not parse: %v\n%s", d, res.Text)
			}
		}
	})
}
