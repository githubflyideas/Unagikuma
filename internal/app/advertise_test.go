package app

import (
	"net"
	"strings"
	"testing"
)

func TestAdvertiseServerURLUnspecifiedListen(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{DataDir: dir, Listen: ":8077", Probe: (&fakeProbes{}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	u := a.advertiseServerURL()
	if !strings.HasPrefix(u, "https://") {
		t.Fatalf("url %q", u)
	}
	host := strings.TrimPrefix(u, "https://")
	h, port, err := net.SplitHostPort(host)
	if err != nil || port != "8077" || h == "" || h == "0.0.0.0" || h == "::" {
		t.Fatalf("host/port from %q: %v %v", u, h, err)
	}
}

func TestAdvertiseServerURLConcreteListen(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{DataDir: dir, Listen: "10.1.2.3:8077", Probe: (&fakeProbes{}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	if got := a.advertiseServerURL(); got != "https://10.1.2.3:8077" {
		t.Fatalf("got %q", got)
	}
}

func TestAdvertiseServerURLBaseURLWins(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{DataDir: dir, Listen: ":8077", BaseURL: "https://mon.example:8443", Probe: (&fakeProbes{}).run})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Store().Close()
	if got := a.advertiseServerURL(); got != "https://mon.example:8443" {
		t.Fatalf("got %q", got)
	}
}
