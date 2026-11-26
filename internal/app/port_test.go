package app

import (
	"errors"
	"testing"
)

// The port is saved for the next start, only when it may be changed here.
func TestSetPort(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	a.opts.Listen = "127.0.0.1:18765"
	if err := a.SetPort(80); err == nil {
		t.Fatal("a port below 1024 was saved")
	}
	if err := a.SetPort(18766); err != nil {
		t.Fatal(err)
	}
	if pi := a.PortInfo(); pi.Current != 18765 || pi.Next != 18766 {
		t.Fatalf("standing: %+v", pi)
	}
	a.opts.PortFixed = true
	if err := a.SetPort(18767); !errors.Is(err, ErrPortFixed) {
		t.Fatalf("a port set with --listen changed: %v", err)
	}
}
