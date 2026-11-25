package app

import (
	"testing"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// TLS checks watch certificate expiry by default; a monitor's own
// thresholds win, and 0 written on it turns one off.
func TestCertDefaults(t *testing.T) {
	ts, _ := tunables.New(nil)
	for _, c := range []struct {
		spec       probe.Spec
		warn, crit string
	}{
		{probe.Spec{Type: "HTTP", Target: "https://example.com/"}, "62", "7"},
		{probe.Spec{Type: "HTTP", Target: "http://example.com/"}, "", ""},
		{probe.Spec{Type: "TCP", Target: "10.0.0.1:993", Opts: map[string]string{"tls": "yes"}}, "62", "7"},
		{probe.Spec{Type: "TCP", Target: "10.0.0.1:22"}, "", ""},
		{probe.Spec{Type: "HTTP", Target: "https://x/", Opts: map[string]string{"cert-days": "30", "cert-critical-days": "0"}}, "30", "0"},
	} {
		certDefaults(&c.spec, ts)
		if c.spec.Opts["cert-days"] != c.warn || c.spec.Opts["cert-critical-days"] != c.crit {
			t.Errorf("%s %s: %v", c.spec.Type, c.spec.Target, c.spec.Opts)
		}
	}
	off, _ := tunables.New(map[string]string{"probe.cert-warn-days": "0", "probe.cert-critical-days": "0"})
	s := probe.Spec{Type: "HTTP", Target: "https://example.com/"}
	certDefaults(&s, off)
	if len(s.Opts) != 0 {
		t.Errorf("defaults turned off: %v", s.Opts)
	}
}

// PING checks fail on heavy loss by default; loss= on the monitor wins,
// and 0 there (or in the tunable) keeps only a total loss failing.
func TestPingDefaults(t *testing.T) {
	ts, _ := tunables.New(nil)
	for _, c := range []struct {
		spec probe.Spec
		want string
	}{
		{probe.Spec{Type: "PING", Target: "10.0.0.1"}, "50%"},
		{probe.Spec{Type: "PING", Target: "10.0.0.1", Opts: map[string]string{"loss": "20%"}}, "20%"},
		{probe.Spec{Type: "PING", Target: "10.0.0.1", Opts: map[string]string{"loss": "0"}}, "0"},
		{probe.Spec{Type: "TCP", Target: "10.0.0.1:22"}, ""},
	} {
		pingDefaults(&c.spec, ts)
		if c.spec.Opts["loss"] != c.want {
			t.Errorf("%s %v: loss %q, want %q", c.spec.Type, c.spec.Opts, c.spec.Opts["loss"], c.want)
		}
	}
	off, _ := tunables.New(map[string]string{"probe.ping-loss": "0"})
	s := probe.Spec{Type: "PING", Target: "10.0.0.1"}
	pingDefaults(&s, off)
	if len(s.Opts) != 0 {
		t.Errorf("default turned off: %v", s.Opts)
	}
}

// share=yes reaches the probe spec as a flag, not as a probe option.
func TestProbeSpecShare(t *testing.T) {
	s, err := ProbeSpec(&config.Monitor{Name: "b", Type: "PING", Target: "10.0.0.5", Opts: map[string]string{"share": "yes", "count": "3"}, Share: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Share || s.Opts["share"] != "" || s.Opts["count"] != "3" {
		t.Fatalf("%+v", s)
	}
	if err := probe.Validate(s); err != nil {
		t.Fatal(err)
	}
}
