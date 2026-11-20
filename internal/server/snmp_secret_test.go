package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// An SNMPv3 check whose passwords are secrets can be saved from the API.
func TestSNMPv3SecretSave(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	for _, n := range []string{"a", "p"} {
		e.do("PUT", "/api/v1/secrets/"+n, map[string]string{"value": "longpassword1"}, nil)
	}
	var er map[string]any
	c := e.do("POST", "/api/v1/monitors", map[string]any{"record": map[string]any{"name": "room", "type": "SNMP", "target": "10.0.0.3",
		"opts": map[string]string{"sensor": "Inlet", "version": "3", "user": "mon", "auth": "sha", "auth-pass": "${secret:a}", "priv": "aes", "priv-pass": "${secret:p}"}}}, &er)
	if c != 200 {
		t.Fatal("not saved")
	}
}

// An option the type does not take is reported when it is saved, also when
// its value is a reference (a warning: older files with one must still
// load); a known option with a reference is kept without one.
func TestUnknownOptionWithReference(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	e.do("PUT", "/api/v1/secrets/tok", map[string]string{"value": "longpassword1"}, nil)
	rec := func(name, typ, target string, opts map[string]string) map[string]any {
		return map[string]any{"record": map[string]any{"name": name, "type": typ, "target": target, "opts": opts}}
	}
	var out map[string]any
	if c := e.do("POST", "/api/v1/monitors", rec("pp", "PING", "10.0.0.9", map[string]string{"bogus": "${secret:tok}"}), &out); c != 200 {
		t.Fatalf("saving: %d", c)
	}
	out = nil
	e.do("GET", "/api/v1/config", nil, &out)
	if b, _ := json.Marshal(out["diagnostics"]); !strings.Contains(string(b), "unknown option bogus") {
		t.Fatalf("no warning: %s", b)
	}
	out = nil
	if c := e.do("POST", "/api/v1/monitors", rec("hh", "HTTP", "http://10.0.0.9/", map[string]string{"header.Authorization": "Bearer ${secret:tok}"}), &out); c != 200 {
		t.Fatalf("known option with a reference refused: %d", c)
	}
	out = nil
	e.do("GET", "/api/v1/config", nil, &out)
	if b, _ := json.Marshal(out["diagnostics"]); strings.Count(string(b), "unknown option") != 1 {
		t.Fatalf("warning for a known option: %s", b)
	}
}
