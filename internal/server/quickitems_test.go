package server

import (
	"strings"
	"testing"
)

func TestQuickTypes(t *testing.T) {
	used := map[string]bool{}
	for _, c := range []struct{ line, typ, target string }{
		{"icmp 9.9.9.9", "PING", "9.9.9.9"}, {"udp 8.8.8.8:53", "UDP", "8.8.8.8:53"}, {"tcp 12.23.23.23:22", "TCP", "12.23.23.23:22"},
		{"http://1.3.5.7:8080", "HTTP", "http://1.3.5.7:8080"}, {"ping 1.1.1.1", "PING", "1.1.1.1"},
		{"https://pay.example.com name=pay title=支付网关 team=admin region=global scope=wan group=payments tags=env:prod,tier:1 policy=default", "HTTP", "https://pay.example.com"},
	} {
		spec, err := quickRecord(c.line, used)
		if err != nil || spec.Type != c.typ || spec.Target != c.target {
			t.Errorf("%s: %+v %v", c.line, spec, err)
		}
		used[spec.Name] = true
	}
	if _, err := quickRecord("ping 1.1.1.1 , udp 8.8.8.8:53", used); err == nil || !strings.Contains(err.Error(), "one target per line") {
		t.Fatalf("two targets on a line: %v", err)
	}
}
