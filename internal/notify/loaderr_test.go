package notify

import "testing"

// The embedded data must load: a broken file would stop the program at
// its start (main refuses to run with LoadError set).
func TestLoadError(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatal(err)
	}
}
