package server

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestDemotionRevokesTokens: an administrator moved to another role loses
// the tokens they made, and the list names each token's owner.
func TestDemotionRevokesTokens(t *testing.T) {
	admin := newEnv(t, usersBind, nil)
	admin.setup()
	var u struct {
		ID int64 `json:"id"`
	}
	if c := admin.do("POST", "/api/v1/users", map[string]any{"username": "second", "password": "secret123", "role": "admin"}, &u); c != 200 {
		t.Fatalf("create admin: %d", c)
	}
	second := admin.loginAs("second", "secret123")
	var tok struct{ Token string }
	if c := second.do("POST", "/api/v1/tokens", map[string]any{"name": "ci", "scopes": []string{"admin"}}, &tok); c != 200 {
		t.Fatalf("token: %d", c)
	}
	bare := &env{t: t, ts: admin.ts, c: &http.Client{Timeout: 10 * time.Second}}
	bearer := []string{"Authorization", "Bearer " + tok.Token}
	if c := bare.do("GET", "/api/v1/tokens", nil, nil, bearer...); c != 200 {
		t.Fatalf("admin token before: %d", c)
	}
	var list struct {
		Items []struct {
			Name     string `json:"name"`
			UserID   int64  `json:"user_id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	admin.do("GET", "/api/v1/tokens", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Username != "second" || list.Items[0].UserID != u.ID {
		t.Fatalf("owner not shown: %+v", list.Items)
	}
	if c := admin.do("PUT", fmt.Sprintf("/api/v1/users/%d", u.ID), map[string]any{"role": "viewer"}, nil); c != 200 {
		t.Fatalf("demote: %d", c)
	}
	if c := bare.do("GET", "/api/v1/monitors", nil, nil, bearer...); c != 401 {
		t.Fatalf("token of a demoted administrator still works: %d", c)
	}
	admin.do("GET", "/api/v1/tokens", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("demoted administrator's tokens kept: %+v", list.Items)
	}
}
