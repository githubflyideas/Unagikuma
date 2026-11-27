package server

import (
	"testing"
)

func TestNormTimes(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":"2026-11-27T20:59:32.859+09:00"}`:       `{"a":"2026-11-27T20:59:32.859+09:00"}`,
		`{"a":"2026-11-27T20:59:32.85Z"}`:             `{"a":"2026-11-27T20:59:32.850Z"}`,
		`{"a":"2026-11-27T20:59:32+09:00"}`:           `{"a":"2026-11-27T20:59:32.000+09:00"}`,
		`{"a":"2026-11-27T20:59:32.123456789-05:00"}`: `{"a":"2026-11-27T20:59:32.123-05:00"}`,
		`{"a":"see 2026-11-27T20:59:32Z here"}`:       `{"a":"see 2026-11-27T20:59:32Z here"}`,
		`{"a":"plain"}`:                               `{"a":"plain"}`,
	} {
		if got := string(normTimes([]byte(in))); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

func TestListEnvelope(t *testing.T) {
	var none []string
	m, ok := listEnvelope(none).(map[string]any)
	if !ok || m["items"] == nil {
		t.Fatalf("nil list: %#v", m)
	}
	if b, _ := encodeAnswer(listEnvelope(none)); string(b) != `{"items":[]}` {
		t.Fatalf("nil list encodes as %s", b)
	}
	if v := listEnvelope(map[string]any{"a": 1}); v.(map[string]any)["a"] != 1 {
		t.Fatal("an object is left alone")
	}
}

// TestListsAndDeletes: every list answers {"items": [...]}, every DELETE
// answers 204 with no body.
func TestListsAndDeletes(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	for _, p := range []string{"/api/v1/tokens", "/api/v1/channels", "/api/v1/tunables", "/api/v1/keywords",
		"/api/v1/languages", "/api/v1/policies", "/api/v1/secrets", "/api/v1/status_pages", "/api/v1/announcements",
		"/api/v1/app_profiles", "/api/v1/channel_kinds", "/api/v1/people", "/api/v1/users"} {
		var items []any
		if c := e.do("GET", p, nil, &items); c != 200 {
			t.Fatalf("%s: %d", p, c)
		}
	}
	var tok struct{ Token string }
	e.do("POST", "/api/v1/tokens", map[string]any{"name": "x", "scopes": []string{"read"}}, &tok)
	var list []struct{ ID int64 }
	e.do("GET", "/api/v1/tokens", nil, &list)
	c, body := e.raw("DELETE", "/api/v1/tokens/"+itoa(list[0].ID), nil, "")
	if c != 204 || len(body) != 0 {
		t.Fatalf("DELETE: %d %q", c, body)
	}
}
