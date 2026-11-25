package server

import "testing"

// pinBind turns the gateway seeding off: the test seeds by hand (the
// records plus their markers in the store, exactly the state the
// seeding leaves) so nothing depends on this machine's routing table.
const pinBind = `@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s suspend.recheck-spread=0s localnet.gateway-monitors=off
web     IN HTTP http://192.168.1.10/ title="Web site" group=public
backup  IN PUSH period=1h
db      IN TCP 192.168.1.20:5432
`

// The seeded gateway monitors hold the first two places of the list —
// the LAN gateway, then the internet gateway — ahead of every order a
// caller can ask for. A record that only carries the name, without the
// seeding behind it, is the user's own and sorts like any other.
func TestGatewayMonitorsPinnedFirst(t *testing.T) {
	e := newEnv(t, pinBind, nil)
	e.setup()
	names := func(path string) []string {
		t.Helper()
		var page struct {
			Items []struct{ Name string }
			Total int
		}
		if c := e.do("GET", path, nil, &page); c != 200 {
			t.Fatalf("GET %s: %d", path, c)
		}
		out := []string{}
		for _, it := range page.Items {
			out = append(out, it.Name)
		}
		if len(out) != page.Total {
			t.Fatalf("GET %s: %d items of %d", path, len(out), page.Total)
		}
		return out
	}
	create := func(name, target string) {
		t.Helper()
		if c := e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": name, "type": "PING", "target": target}}, nil); c != 200 {
			t.Fatalf("create %s: %d", name, c)
		}
	}

	// A user monitor created before the gateways are seeded.
	create("aaa-first", "192.168.1.30")
	// The gateway records, not yet seeded: the name alone does not pin.
	create("internet-gateway", "8.8.8.8")
	create("lan-gateway", "192.168.1.1")
	if got := names("/api/v1/monitors?sort=name&order=desc"); got[0] == "lan-gateway" {
		t.Fatalf("an unseeded record holds the head of the list: %v", got)
	}
	// Seed them: the markers the seeding stores for the addresses.
	if err := e.app.Store().SetSetting("auto_monitor_lan_gateway", "192.168.1.1"); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Store().SetSetting("auto_monitor_internet_gateway", "8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	// A user monitor created after the seeding.
	create("zzz-last", "192.168.1.31")

	for _, path := range []string{
		"/api/v1/monitors",
		"/api/v1/monitors?sort=name",
		"/api/v1/monitors?sort=name&order=desc",
		"/api/v1/monitors?sort=latency",
		"/api/v1/monitors?sort=uptime&order=desc",
		"/api/v1/monitors?sort=last_change",
	} {
		got := names(path)
		if len(got) != 7 || got[0] != "lan-gateway" || got[1] != "internet-gateway" {
			t.Fatalf("%s: %v", path, got)
		}
	}
}
