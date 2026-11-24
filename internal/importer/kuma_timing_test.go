package importer

import (
	"strings"
	"testing"
)

// The timing of a Kuma monitor is carried as written: the retry interval
// also when it equals the check interval (the common case in real
// exports), the timeout also when it is not shorter than the interval,
// and a resend setting or a missing interval is never dropped quietly.
func TestKumaTimingCarried(t *testing.T) {
	backup := func(mon string) []byte {
		return []byte(`{"version":"1.23.17","monitorList":[` + mon + `],"notificationList":[]}`)
	}
	httpMon := func(extra string) string {
		return `{"id":1,"name":"svc","type":"http","url":"https://example.invalid/","interval":60,"retryInterval":60,"maxretries":2,"timeout":48,"active":true` + extra + `}`
	}

	t.Run("retry equal to interval is written", func(t *testing.T) {
		res, err := Kuma(backup(httpMon("")), nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Text, "retry=1m") {
			t.Fatalf("retry= missing from the record:\n%s", res.Text)
		}
	})

	t.Run("timeout not shorter than the interval is written as it was", func(t *testing.T) {
		// interval 20s, timeout 48s: the old importer silently rewrote
		// the timeout to 16s. The configuration only warns about a
		// timeout that is not shorter than the interval, so the value
		// is carried unchanged.
		mon := `{"id":1,"name":"router","type":"http","url":"https://example.invalid/","interval":20,"retryInterval":20,"maxretries":0,"timeout":48,"active":true}`
		res, err := Kuma(backup(mon), nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Text, "timeout=48s") || strings.Contains(res.Text, "timeout=16s") {
			t.Fatalf("timeout rewritten:\n%s", res.Text)
		}
	})

	t.Run("resend interval is reported, not dropped", func(t *testing.T) {
		res, err := Kuma(backup(httpMon(`,"resendInterval":3`)), nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		notes := strings.Join(res.Notes, "\n")
		if !strings.Contains(notes, "resendInterval") || !strings.Contains(notes, "not carried over") {
			t.Fatalf("resend setting vanished quietly:\n%s", notes)
		}
	})

	t.Run("a monitor without an interval is reported with the default it inherits", func(t *testing.T) {
		mon := `{"id":1,"name":"svc","type":"http","url":"https://example.invalid/","interval":0,"retryInterval":0,"maxretries":0,"timeout":10,"active":true}`
		res, err := Kuma(backup(mon), nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		notes := strings.Join(res.Notes, "\n")
		if !strings.Contains(notes, "no check interval") || !strings.Contains(notes, "(1m)") {
			t.Fatalf("missing interval not reported with its default:\n%s", notes)
		}
	})
}
