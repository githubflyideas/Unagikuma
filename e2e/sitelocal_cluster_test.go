//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A node's own monitors reach the master: a copy of its configuration is
// kept there (never applied) and its monitors count toward the cluster's
// limit; the node learns the cluster's standing and refuses its own
// additions past it.
func TestSiteLocalReachesMaster(t *testing.T) {
	a := start(t, "a", fmt.Sprintf(soaRT, "a"))
	a.setup("admin", "master-pass-1")
	b := start(t, "b", fmt.Sprintf(soaRT, "b"))
	b.setup("local", "node-pass-1")
	join(t, a, b, "b")

	usage := func(s *server) (used, limit int) {
		var r struct {
			Monitors     struct{ Used, Limit int } `json:"monitors"`
			Usage        struct{ Monitors int }    `json:"usage"`
			MonitorLimit int                       `json:"monitor_limit"`
		}
		s.ensure()
		s.call("GET", "/api/v1/license", nil, &r)
		return r.Usage.Monitors, r.MonitorLimit
	}
	before, _ := usage(a)
	var zone strings.Builder
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&zone, "own%d IN TCP 127.0.0.1:%d\n", i, 20000+i)
	}
	b.ensure()
	if code := b.call("PUT", "/api/v1/cluster/site-local", map[string]string{"text": zone.String()}, nil); code != 200 {
		t.Fatalf("b's own configuration: %d", code)
	}
	a.waitFor("b's own monitors counted on the master", func() bool { n, _ := usage(a); return n == before+7 })
	a.waitFor("a copy of b's own configuration on the master", func() bool {
		data, err := os.ReadFile(filepath.Join(a.dir, "sites", "b.local.monitors.conf"))
		return err == nil && strings.Contains(string(data), "own7")
	})
	b.waitFor("the node shows the cluster's standing", func() bool { n, l := usage(b); return n == before+7 && l == 100 })
	// Past the cluster's limit, the node refuses its own additions.
	var big strings.Builder
	for i := 1; i <= 120; i++ {
		fmt.Fprintf(&big, "own%d IN TCP 127.0.0.1:%d\n", i, 20000+i)
	}
	var res struct{ OK bool }
	b.call("PUT", "/api/v1/cluster/site-local", map[string]string{"text": big.String()}, &res)
	if res.OK {
		t.Fatal("the node accepted its own monitors past the cluster's limit")
	}
}
