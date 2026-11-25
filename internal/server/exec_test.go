package server

import "testing"

func TestExecChecksNeedAdmin(t *testing.T) {
	admin := newEnv(t, usersBind+"disk IN EXEC check_disk args=\"-w 20% -c 10%\"\n", nil)
	admin.setup()
	if c := admin.do("POST", "/api/v1/users", map[string]any{"username": "ops", "password": "secret123", "role": "operator"}, nil); c != 200 {
		t.Fatalf("create operator: %d", c)
	}
	ops := admin.loginAs("ops", "secret123")
	exec := map[string]any{"record": map[string]any{"name": "load", "type": "EXEC", "target": "check_load"}}
	for _, req := range [][3]any{
		{"POST", "/api/v1/monitors", exec},
		{"POST", "/api/v1/probe", exec},
		{"PUT", "/api/v1/monitors/disk", map[string]any{"record": map[string]any{"type": "EXEC", "target": "check_disk", "opts": map[string]string{"args": "-w 1%"}}}},
		{"PUT", "/api/v1/monitors/web", map[string]any{"record": map[string]any{"type": "EXEC", "target": "check_http"}}},
	} {
		if c := ops.do(req[0].(string), req[1].(string), req[2], nil); c != 403 {
			t.Fatalf("%s %s by an operator: %d", req[0], req[1], c)
		}
	}
	var cfg struct{ Text string }
	ops.do("GET", "/api/v1/config", nil, &cfg)
	if c := ops.do("PUT", "/api/v1/config", map[string]any{"text": cfg.Text + "x IN EXEC check_x\n"}, nil); c != 403 {
		t.Fatalf("config text with a new EXEC by an operator: %d", c)
	}
	// Operators keep the rest of their work.
	if c := ops.do("POST", "/api/v1/monitors/disk/pause", map[string]any{"paused": true}, nil); c != 200 {
		t.Fatalf("pause: %d", c)
	}
	if c := ops.do("PUT", "/api/v1/config", map[string]any{"text": cfg.Text + "gw IN PING 10.0.0.1\n"}, nil); c != 200 {
		t.Fatalf("unrelated config change: %d", c)
	}
	if c := admin.do("POST", "/api/v1/monitors", exec, nil); c != 200 {
		t.Fatalf("admin: %d", c)
	}
}
