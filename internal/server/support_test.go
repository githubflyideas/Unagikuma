package server

import (
	"strings"
	"testing"

	"github.com/githubflyideas/unagikuma/internal/license"
)

// The status page footer line is the registered edition's: unregistered,
// it cannot be changed and the usual line stays.
func TestStatusSupportFooter(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	if c := e.do("PUT", "/api/v1/status-support", map[string]string{"text": "Support: ops@example.com"}, nil); c == 200 {
		t.Fatal("unregistered: footer changed")
	}
	e.register(license.FeatBranding)
	if c := e.do("PUT", "/api/v1/status-support", map[string]string{"text": strings.Repeat("x", 121)}, nil); c != 400 {
		t.Fatalf("too long: %d", c)
	}
	if c := e.do("PUT", "/api/v1/status-support", map[string]string{"text": "Support: ops@example.com"}, nil); c != 200 {
		t.Fatalf("save: %d", c)
	}
	var pub struct{ Support string }
	if c := e.do("GET", "/api/v1/status/main", nil, &pub); c != 200 || pub.Support != "Support: ops@example.com" {
		t.Fatalf("public: %d %+v", c, pub)
	}
}
