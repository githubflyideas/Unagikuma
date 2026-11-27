package app

import "testing"

func TestDefaultInstanceName(t *testing.T) {
	cases := []struct {
		dir, listen, host, want string
	}{
		{"data", ":8077", "box", "box"},
		{"/var/lib/unagikuma", "127.0.0.1:8077", "box", "box"},
		{"/var/lib/unagikuma/shop", ":8077", "box", "box-shop"},
		{"/var/lib/unagikuma/shop", ":8078", "box", "box-shop"},
		{"data", ":8078", "box", "box-8078"},
		{"./data", "0.0.0.0:8099", "box", "box-8099"},
		{"data", ":8077", "", "unagikuma"},
	}
	for _, c := range cases {
		if got := DefaultInstanceName(c.dir, c.listen, c.host); got != c.want {
			t.Errorf("DefaultInstanceName(%q, %q, %q) = %q, want %q", c.dir, c.listen, c.host, got, c.want)
		}
	}
}
