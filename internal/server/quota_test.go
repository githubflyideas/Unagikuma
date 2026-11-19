package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/license"
)

func TestAccountQuota(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	add := func(name string, want int) {
		t.Helper()
		var er struct{ Code string }
		if code := e.do("POST", "/api/v1/users", map[string]any{"username": name, "password": "secret123", "role": "operator"}, &er); code != want {
			t.Fatalf("add %s: %d %+v, want %d", name, code, er, want)
		}
		if want == 402 && er.Code != CodeQuota {
			t.Fatalf("code %q", er.Code)
		}
	}
	// Unregistered: the administrator and four more, the sixth is refused.
	for i := 1; i <= license.FreeUsers-1; i++ {
		add("op"+itoa(int64(i)), 200)
	}
	add("op-over", 402)
	if code := e.do("GET", "/api/v1/users", nil, nil); code != 200 {
		t.Fatalf("list: %d", code)
	}
	// Nodes are never limited.
	var er struct{ Code string }
	if code := e.do("POST", "/api/v1/nodes", map[string]any{"name": "bj"}, &er); code == 402 {
		t.Fatalf("nodes are limited: %+v", er)
	}
	install := func(f license.File) {
		t.Helper()
		f.Customer, f.Licensee, f.Product, f.License = "Co", license.LicenseeOrganization, license.Product, license.TypePerpetual
		b, _ := json.Marshal(license.Sign(testLicenseKey, f))
		if _, err := e.app.License().Install(b, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// A licence never gives less than the free number...
	install(license.File{MaxUsers: 2})
	add("op-a", 402)
	// ...a larger number is what it says...
	install(license.File{MaxUsers: license.FreeUsers + 1})
	add("op-b", 200)
	add("op-c", 402)
	// ...and none is no limit.
	install(license.File{})
	add("op-d", 200)
}
