package app

import (
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
)

// The internet gateway gets a ping monitor once; it follows the address,
// keeps the user's settings and stays deleted once deleted.
func TestGatewayMonitors(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.store.Close()
	setExit := func(addr string) {
		t.Helper()
		if _, err := a.EditConfig("alice", "exit", func(f *config.File) error {
			return f.SetTunables(map[string]string{"localnet.exit-target": addr}, nil)
		}); err != nil {
			t.Fatal(err)
		}
		a.ensureGatewayMonitors()
	}
	record := func() (config.Record, bool) {
		f, _ := config.Parse("x", []byte(a.Config().Text))
		return f.Record("internet-gateway")
	}
	setExit("114.114.114.114")
	r, ok := record()
	if !ok || r.Type != "PING" || r.Target != "114.114.114.114" || r.Interval != "1m" {
		t.Fatalf("record %+v %v", r, ok)
	}
	// The user raises the attempts; the gateway moves; the attempts stay.
	if _, err := a.EditConfig("alice", "attempts", func(f *config.File) error {
		return f.SetOpts("internet-gateway", map[string]string{"attempts": "3"})
	}); err != nil {
		t.Fatal(err)
	}
	setExit("223.5.5.5")
	r, _ = record()
	attempts := ""
	for _, o := range r.Opts {
		if o.Key == "attempts" {
			attempts = o.Value
		}
	}
	if r.Target != "223.5.5.5" || attempts != "3" {
		t.Fatalf("after the move %+v", r)
	}
	// Deleted by the user: not added again, even for another address.
	if _, err := a.EditConfig("alice", "delete", func(f *config.File) error { return f.DeleteRecord("internet-gateway") }); err != nil {
		t.Fatal(err)
	}
	setExit("8.8.8.8")
	if _, ok := record(); ok {
		t.Fatal("added again after it was deleted")
	}
}
