package server

import "testing"

// TestPreferencesValidated: an account's preferences hold its style,
// language and time zone, each a short name, and nothing else.
func TestPreferencesValidated(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	for body, want := range map[string]int{
		`{"style":"noc","lang":"ja","timezone":"Asia/Tokyo"}`: 200,
		`{"style":""}`:                             200,
		`{"evil":"x"}`:                             400,
		`{"style":{"nested":true}}`:                400,
		`{"timezone":"<script>alert(1)</script>"}`: 400,
	} {
		c, _ := e.raw("PUT", "/api/v1/preferences", []byte(body), "application/json")
		if c != want {
			t.Errorf("%s: %d, want %d", body, c, want)
		}
	}
	big := make([]byte, 0, 8<<10)
	big = append(big, `{"style":"`...)
	for len(big) < 8<<10 {
		big = append(big, 'a')
	}
	big = append(big, `"}`...)
	if c, _ := e.raw("PUT", "/api/v1/preferences", big, "application/json"); c != 413 && c != 400 {
		t.Errorf("8 KiB of preferences: %d", c)
	}
}
