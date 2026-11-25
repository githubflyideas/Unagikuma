package importer

import (
	"github.com/githubflyideas/unagikuma/internal/config"
	"os"
	"testing"
)

func FuzzBoundedAdapters(f *testing.F) {
	for _, p := range []string{"testdata/gatus.yaml", "testdata/checkmate.json"} {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte("endpoints: &a [*a]"))
	f.Add([]byte(`[{"name":"duplicate","name":"duplicate"}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, convert := range []func([]byte, *config.Config, Options) (*Result, error){Gatus, Checkmate} {
			r, err := convert(data, nil, Options{})
			if err != nil {
				continue
			}
			_, _, ds := config.Load("fuzz.bind", []byte("@ IN SOA ns admin.local ( 1 1m 15s 5s 3 )\n"+r.Text), config.Options{SecretKnown: func(string) bool { return true }})
			if config.HasErrors(ds) {
				t.Fatalf("generated invalid config: %v", ds)
			}
		}
	})
}
