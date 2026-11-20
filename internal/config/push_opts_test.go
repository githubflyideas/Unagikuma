package config

import (
	"strings"
	"testing"
)

// Value rules and unknown options on a PUSH monitor are errors: nothing
// would ever judge them.
func TestPushRefusesCheckOptions(t *testing.T) {
	for _, line := range []string{"p IN PUSH period=1m warn=5", "p IN PUSH period=1m crit=banana", "p IN PUSH period=1m totally-unknown=1"} {
		_, _, d := Load("monitors.conf", []byte("$FILE revision=1\n"+line+"\n"), Options{})
		if !HasErrors(d) || !strings.Contains(diagText(d), "PUSH monitor takes no") {
			t.Errorf("%q accepted: %v", line, d)
		}
	}
	_, _, d := Load("monitors.conf", []byte("$FILE revision=1\np IN PUSH period=1m grace=30s title=\"Backup\" team=admin\n"), Options{})
	if HasErrors(d) {
		t.Fatalf("plain push refused: %v", d)
	}
}

func diagText(d []Diagnostic) string {
	var b strings.Builder
	for _, x := range d {
		b.WriteString(x.Msg + "\n")
	}
	return b.String()
}

// A service target written as a URL gets one clear error, not a second one
// about a type nothing could be checked for.
func TestServiceTargetAsURLOneError(t *testing.T) {
	_, _, d := Load("monitors.conf", []byte("$FILE revision=1\ndb IN POSTGRES postgres://app@10.0.0.5:5432/app query=\"SELECT 1\"\n"), Options{})
	n := 0
	for _, x := range d {
		if x.Severity == SevError {
			n++
			if !strings.Contains(x.Msg, "host and port only") {
				t.Errorf("unhelpful: %s", x.Msg)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d errors: %v", n, d)
	}
}
