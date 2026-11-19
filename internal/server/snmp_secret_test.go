package server

import "testing"

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
