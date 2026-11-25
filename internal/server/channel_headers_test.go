package server

import (
	"strings"
	"testing"
)

func TestChannelHeadersKeptAndCredentialsStoredAsSecrets(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	opts := map[string]string{"url": "https://hooks.example.com/x", "header.content-type": "application/json", "header.authorization": "Bearer abc", "retries": "1"}
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "wh", "kind": "webhook", "opts": opts}, nil); c != 200 {
		t.Fatalf("save: %d", c)
	}
	ch := e.app.EffectiveConfig().FindChannel("wh")
	if ch == nil || ch.Opts["header.content-type"] != "application/json" || !strings.HasPrefix(ch.Opts["header.authorization"], "${secret:") {
		t.Fatalf("stored: %+v", ch)
	}
	var list []struct {
		Name string            `json:"name"`
		Opts map[string]string `json:"opts"`
	}
	e.do("GET", "/api/v1/channels", nil, &list)
	var got map[string]string
	for _, c := range list {
		if c.Name == "wh" {
			got = c.Opts
		}
	}
	// The list shows the secret's reference, never the credential.
	if got["header.authorization"] != "${secret:ch_wh_header_authorization}" || got["header.content-type"] != "application/json" {
		t.Fatalf("listed: %v", got)
	}
	// The form sends the masked value back: the saved credential stays.
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "wh", "kind": "webhook", "opts": got}, nil); c != 200 {
		t.Fatalf("resave: %d", c)
	}
	if v := e.app.EffectiveConfig().FindChannel("wh").Opts["header.authorization"]; !strings.HasPrefix(v, "${secret:") {
		t.Fatalf("credential lost: %q", v)
	}
}
