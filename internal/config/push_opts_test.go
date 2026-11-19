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
