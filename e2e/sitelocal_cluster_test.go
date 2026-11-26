//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A node's own configuration reaches the master as a copy (never applied,
// counted toward no limit); the node judges it by its own limit, 100.
func TestSiteLocalReachesMaster(t *testing.T) {
	a := start(t, "a", fmt.Sprintf(soaRT, "a"))
	a.setup("admin", "master-pass-1")
	b := start(t, "b", fmt.Sprintf(soaRT, "b"))
	b.setup("local", "node-pass-1")
	join(t, a, b, "b")

	var zone strings.Builder
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&zone, "own%d IN TCP 127.0.0.1:%d\n", i, 20000+i)
	}
	b.ensure()
	if code := b.call("PUT", "/api/v1/cluster/site-local", map[string]string{"text": zone.String()}, nil); code != 200 {
		t.Fatalf("b's own configuration: %d", code)
	}
	a.waitFor("a copy of b's own configuration on the master", func() bool {
		data, err := os.ReadFile(filepath.Join(a.dir, "sites", "b.local.monitors.conf"))
		return err == nil && strings.Contains(string(data), "own7")
	})
	// Past the site's own limit, the node refuses its own additions.
	var big strings.Builder
	for i := 1; i <= 101; i++ {
		fmt.Fprintf(&big, "own%d IN TCP 127.0.0.1:%d\n", i, 20000+i)
	}
	var res struct{ OK bool }
	b.call("PUT", "/api/v1/cluster/site-local", map[string]string{"text": big.String()}, &res)
	if res.OK {
		t.Fatal("the node accepted 101 of its own monitors")
	}
}
