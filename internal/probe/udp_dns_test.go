package probe

import "testing"

// An address or a name is the same or another one; an MX or TXT answer may
// be named by a part of it.
func TestDNSExpectExact(t *testing.T) {
	for _, c := range []struct {
		qtype  string
		texts  []string
		expect string
		ok     bool
	}{
		{"A", []string{"10.0.0.10"}, "10.0.0.1", false},
		{"A", []string{"10.0.0.1"}, "10.0.0.1", true},
		{"A", []string{"110.0.0.1", "10.0.0.2"}, "10.0.0.1", false},
		{"AAAA", []string{"2001:db8::1"}, "2001:0db8:0:0:0:0:0:1", true}, // the same address, spelled differently
		{"CNAME", []string{"Www.Example.com."}, "www.example.com", true},
		{"CNAME", []string{"a.www.example.com."}, "www.example.com", false},
		{"MX", []string{"10 mail.example.com."}, "mail.example.com", true},
		{"TXT", []string{"v=spf1 include:_spf.example.com ~all"}, "v=spf1", true},
	} {
		got := answersProblem(c.texts, c.expect, "", exactAnswers(c.qtype)) == ""
		if got != c.ok {
			t.Errorf("%s %v expect %q: matched=%v", c.qtype, c.texts, c.expect, got)
		}
	}
}
