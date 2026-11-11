package config

import "testing"

// A bare host:port on standby= is read as the node's own TLS port, with a
// warning; a URL with another scheme is still an error.
func TestStandbyWithoutScheme(t *testing.T) {
	_, cfg, ds := load(t, "$FILE revision=2026111101\nbk IN NODE standby=127.0.0.1:8077\n")
	if errs := errorsOf(ds); len(errs) > 0 {
		t.Fatalf("bare host:port refused: %v", errs)
	}
	if got := cfg.FindNode("bk").Standby; got != "https://127.0.0.1:8077" {
		t.Fatalf("standby = %q", got)
	}
	warned := false
	for _, d := range ds {
		warned = warned || d.Code == "standby_scheme_added"
	}
	if !warned {
		t.Fatal("no standby_scheme_added warning")
	}
	for _, bad := range []string{"http://127.0.0.1:8077", "https://127.0.0.1:8077/x", "127.0.0.1:"} {
		if _, _, ds := load(t, "$FILE revision=2026111101\nbk IN NODE standby="+bad+"\n"); len(errorsOf(ds)) == 0 {
			t.Errorf("standby=%s accepted", bad)
		}
	}
}
