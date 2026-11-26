package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/license"
)

// The monitor limit on the licence surface and at the create door: the
// free 100, a version 4 licence's own number, and no field = no limit.
func TestLicenseMonitorCap(t *testing.T) {
	e := newEnv(t, usersBind, nil)
	e.setup()

	var lic struct {
		Registered   bool `json:"registered"`
		MonitorLimit int  `json:"monitor_limit"`
		FreeMonitors int  `json:"free_monitors"`
		Usage        struct {
			Monitors int `json:"monitors"`
		} `json:"usage"`
	}
	if c := e.do("GET", "/api/v1/license", nil, &lic); c != 200 || lic.Registered || lic.MonitorLimit != 100 || lic.FreeMonitors != 300 {
		t.Fatalf("unregistered licence surface: %d %+v", c, lic)
	}
	// usersBind is a cluster (node bj): the shared configuration's limit
	// is 100. Fill to it in one structured edit ("web" and "bj-core" are
	// already there).
	var block strings.Builder
	for i := 1; i <= 98; i++ {
		fmt.Fprintf(&block, "cap%03d IN PING 10.0.0.%d\n", i, i%250+1)
	}
	if _, err := e.app.EditConfig("test", "fill", func(f *config.File) error {
		return f.AppendBlock(block.String())
	}); err != nil {
		t.Fatalf("fill to 100: %v", err)
	}
	var er struct {
		Code        string
		Diagnostics []struct{ Code string } `json:"diagnostics"`
	}
	// Refused as a quota, like the account limit: 402 quota_exceeded.
	if c := e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "one-more", "type": "PING", "target": "10.9.9.9"}}, &er); c != 402 {
		t.Fatalf("101st create: %d", c)
	}
	if er.Code != CodeQuota || len(er.Diagnostics) != 1 || er.Diagnostics[0].Code != "monitor_limit" {
		t.Fatalf("101st create refusal: %+v", er)
	}
	// A version 4 licence with max_monitors raises the limit to its own.
	install := func(f license.File) {
		t.Helper()
		b, _ := json.Marshal(license.Sign(testLicenseKey, f))
		if _, err := e.app.License().Install(b, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	install(license.File{Customer: "Cap KK", Licensee: license.LicenseeOrganization, Product: license.Product,
		License: license.TypePerpetual, MaxMonitors: 150})
	if c := e.do("GET", "/api/v1/license", nil, &lic); c != 200 || !lic.Registered || lic.MonitorLimit != 150 || lic.Usage.Monitors != 100 {
		t.Fatalf("capped licence surface: %d %+v", c, lic)
	}
	if c := e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "one-more", "type": "PING", "target": "10.9.9.9"}}, nil); c != 200 {
		t.Fatalf("101st under a 150 licence: %d", c)
	}
	// A version 4 licence without the field lifts the limit entirely.
	install(license.File{Customer: "Open KK", Licensee: license.LicenseeOrganization, Product: license.Product,
		License: license.TypePerpetual})
	if c := e.do("GET", "/api/v1/license", nil, &lic); c != 200 || lic.MonitorLimit != 0 {
		t.Fatalf("uncapped licence surface: %d %+v", c, lic)
	}
}
