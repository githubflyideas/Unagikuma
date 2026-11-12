package server

import "testing"

func TestQuickItems(t *testing.T) {
	got := quickItems("ping 1.1.1.1 , udp 8.8.8.8:53 , tcp 12.23.23.23:22 , http://1.3.5.7:8080 ，icmp 9.9.9.9\n; a comment\nhttps://a.example.com tags=env:prod,tier:1\nx.example.com;y.example.com")
	want := []string{"ping 1.1.1.1", "udp 8.8.8.8:53", "tcp 12.23.23.23:22", "http://1.3.5.7:8080", "icmp 9.9.9.9", "https://a.example.com tags=env:prod,tier:1", "x.example.com;y.example.com"}
	if len(got) != len(want) {
		t.Fatalf("%d items: %+v", len(got), got)
	}
	for i := range want {
		if got[i].text != want[i] {
			t.Errorf("item %d: %q, want %q", i, got[i].text, want[i])
		}
	}
	used := map[string]bool{}
	for _, c := range []struct{ line, typ, target string }{
		{"icmp 9.9.9.9", "PING", "9.9.9.9"}, {"udp 8.8.8.8:53", "UDP", "8.8.8.8:53"}, {"tcp 12.23.23.23:22", "TCP", "12.23.23.23:22"},
		{"http://1.3.5.7:8080", "HTTP", "http://1.3.5.7:8080"}, {"ping 1.1.1.1", "PING", "1.1.1.1"},
	} {
		spec, err := quickRecord(c.line, used)
		if err != nil || spec.Type != c.typ || spec.Target != c.target {
			t.Errorf("%s: %+v %v", c.line, spec, err)
		}
		used[spec.Name] = true
	}
}
