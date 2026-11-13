package server

import (
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// A line typed the quick-add way gets the full record as a suggestion.
func TestRecordSuggestions(t *testing.T) {
	text := "$FILE revision=1\nping 8.8.4.4\nrouter IN PING 192.0.2.1\nnot a thing at all here\n"
	_, _, diags := config.Load("local.monitors.conf", []byte(text), config.Options{})
	got := recordSuggestions(text, diags)
	if len(got) != 1 || got[0].Line != 2 || !strings.Contains(got[0].Text, " IN PING 8.8.4.4") {
		t.Fatalf("suggestions %+v (diags %v)", got, diags)
	}
	fixed := strings.Replace(text, "ping 8.8.4.4", got[0].Text, 1)
	fixed = strings.Replace(fixed, "not a thing at all here\n", "", 1)
	if _, _, d := config.Load("local.monitors.conf", []byte(fixed), config.Options{}); config.HasErrors(d) {
		t.Fatalf("suggested record does not load: %v", d)
	}
}
