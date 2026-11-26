package main

import (
	"reflect"
	"testing"
)

func TestPeelServiceName(t *testing.T) {
	cases := []struct {
		in   []string
		name string
		rest []string
	}{
		{nil, "", nil},
		{[]string{"server", "--data", "C:\\data"}, "", []string{"server", "--data", "C:\\data"}},
		{[]string{"--service-name", "shop", "server", "--data", "D:\\shop"}, "shop", []string{"server", "--data", "D:\\shop"}},
		{[]string{"--service-name=ops", "server"}, "ops", []string{"server"}},
	}
	for _, c := range cases {
		name, rest := peelServiceName(c.in)
		if name != c.name || !strEq(rest, c.rest) {
			t.Errorf("peelServiceName(%q) = %q %q, want %q %q", c.in, name, rest, c.name, c.rest)
		}
	}
}

func strEq(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
