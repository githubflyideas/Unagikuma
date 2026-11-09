package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/license"
)

func TestLicenceQuotas(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	// Unregistered: no limit.
	if code := e.do("POST", "/api/v1/users", map[string]any{"username": "op1", "password": "secret123", "role": "operator"}, nil); code != 200 {
		t.Fatalf("unregistered add: %d", code)
	}
	f := license.Sign(testLicenseKey, license.File{Customer: "Small", Licensee: license.LicenseeOrganization, Product: license.Product,
		License: license.TypePerpetual, Tier: license.TierTeam, MaxUsers: 2, MaxNodes: 1})
	b, _ := json.Marshal(f)
	if _, err := e.app.License().Install(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	var st struct {
		MaxUsers int `json:"max_users"`
		Usage    struct{ Nodes, Users int }
	}
	if code := e.do("GET", "/api/v1/license", nil, &st); code != 200 || st.MaxUsers != 2 || st.Usage.Users != 2 || st.Usage.Nodes != 1 {
		t.Fatalf("license: %d %+v", code, st)
	}
	var er struct{ Code string }
	if code := e.do("POST", "/api/v1/users", map[string]any{"username": "op2", "password": "secret123", "role": "operator"}, &er); code != 402 || er.Code != CodeQuota {
		t.Fatalf("over the account quota: %d %+v", code, er)
	}
	if code := e.do("POST", "/api/v1/nodes", map[string]any{"name": "bj"}, &er); code != 402 || er.Code != CodeQuota {
		t.Fatalf("over the node quota: %d %+v", code, er)
	}
	// Existing accounts keep working.
	if code := e.do("GET", "/api/v1/users", nil, nil); code != 200 {
		t.Fatalf("list: %d", code)
	}
}
