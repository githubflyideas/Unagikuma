package config

import (
	"strings"
	"testing"
)

// Two monitors that check exactly the same thing are pointed out (the
// target gets every request twice); share=yes on the later one settles
// it. Same host, other port, options or nodes: a different check.
func TestSameCheckWarnings(t *testing.T) {
	soa := "@ IN SOA m. a.b. ( 1 1m 15s 5s 3 )\nc IN CHANNEL log\ndefault IN POLICY chain=c\nteam-b IN POLICY chain=c\n"
	codes := func(src string) map[string][]string {
		_, _, ds := load(t, soa+src)
		if e := errorsOf(ds); len(e) > 0 {
			t.Fatalf("%q: errors %v", src, e)
		}
		out := map[string][]string{}
		for _, d := range ds {
			if d.Code == "same_check" || d.Code == "share_unused" {
				out[d.Code] = append(out[d.Code], d.Msg)
			}
		}
		return out
	}
	for src, want := range map[string]string{
		"a IN PING 10.0.0.5\nb IN PING 10.0.0.5 policy=team-b title=B\n":          "same_check",
		"a IN PING Host.Example\nb IN PING host.example\n":                        "same_check",
		"a IN HTTP https://Example.com/x\nb IN HTTP https://example.com/x\n":      "same_check",
		"a IN TCP 10.0.0.5:22\nb IN TCP 10.0.0.5:22 group=ops tags=team:b\n":      "same_check",
		"a IN UDP 10.0.0.5:snmp\nb IN UDP 10.0.0.5:161\n":                         "same_check",
		"a IN PING 10.0.0.5\nb IN PING 10.0.0.5 share=yes\n":                      "",
		"a IN PING 10.0.0.5\nb IN PING 10.0.0.6\n":                                "",
		"a IN TCP 10.0.0.5:22\nb IN TCP 10.0.0.5:23\n":                            "",
		"a IN PING 10.0.0.5\nb IN PING 10.0.0.5 count=5\n":                        "",
		"a IN PING 10.0.0.5\nb IN PING 10.0.0.5 timeout=2s\n":                     "",
		"a IN PING 10.0.0.5\nb IN PING 10.0.0.5 disabled=yes\n":                   "",
		"a IN HTTP https://example.com/x\nb IN HTTP https://example.com/y\n":      "",
		"a IN PING 10.0.0.5 share=yes\n":                                          "share_unused",
		"a IN PING 10.0.0.5\nb IN PING 10.0.0.5 share=yes\nc2 IN PING 10.0.0.5\n": "same_check",
	} {
		got := codes(src)
		switch {
		case want == "" && len(got) > 0:
			t.Errorf("%q: unexpected %v", src, got)
		case want != "" && len(got[want]) == 0:
			t.Errorf("%q: want %s, got %v", src, want, got)
		case want == "same_check" && !strings.Contains(got[want][0], "share=yes"):
			t.Errorf("%q: the warning does not say what to do: %v", src, got)
		}
	}
	_, _, ds := load(t, soa+"a IN PING 10.0.0.5 share=maybe\nb IN PING 10.0.0.5\n")
	if len(errorsOf(ds)) == 0 {
		t.Error("share=maybe accepted")
	}
}
