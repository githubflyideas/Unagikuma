package main

import (
	"strings"
	"testing"
)

func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := randomPassword()
		if len(p) != 20 || strings.ContainsAny(p, "0O1lI") {
			t.Fatalf("bad password %q", p)
		}
		if seen[p] {
			t.Fatal("repeated password")
		}
		seen[p] = true
	}
}

func TestInterleavedFlags(t *testing.T) {
	got := interleavedFlags([]string{"kuma.db", "--dry-run", "--data", "d", "x"}, map[string]bool{"data": true})
	if strings.Join(got, " ") != "--dry-run --data d kuma.db x" {
		t.Fatalf("%v", got)
	}
}
