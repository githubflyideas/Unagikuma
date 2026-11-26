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
	if c := e.do("GET", "/api/v1/license", nil, &lic); c != 200 || lic.Registered || lic.MonitorLimit != 100 || lic.FreeMonitors != 100 {
		t.Fatalf("unregistered licence surface: %d %+v", c, lic)
	}
	// Fill to the free limit in one structured edit (usersBind already
	// runs one counted monitor, "web"; "bj-core" runs on bj only).
	var block strings.Builder
	for i := 1; i <= 99; i++ {
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

// A first start past the import ceiling monitors nothing; installing a
// licence starts monitoring at once, without a restart.
func TestFirstStartPastCeilingStartsOnRegistration(t *testing.T) {
	var b strings.Builder
	b.WriteString("@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )\n")
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&b, "m%03d IN PING 10.0.%d.%d\n", i, i/250, i%250+1)
	}
	e := newEnv(t, b.String(), nil)
	e.setup()
	if n := len(e.app.Config().Config.Monitors); n != 0 {
		t.Fatalf("first start past the ceiling ran %d monitors", n)
	}
	f := license.Sign(testLicenseKey, license.File{Customer: "Test KK", Licensee: license.LicenseeOrganization, Product: license.Product, License: license.TypePerpetual})
	if c := e.do("PUT", "/api/v1/license", f, nil); c != 200 {
		t.Fatalf("install: %d", c)
	}
	if n := len(e.app.Config().Config.Monitors); n != 250 {
		t.Fatalf("after registering: %d monitors, want 250", n)
	}
}
