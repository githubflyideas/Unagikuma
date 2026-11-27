package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postAs sends one POST from the given source address through the
// whole handler chain and returns the recorder.
func postAs(e *env, ip, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	req.RemoteAddr = ip + ":4000"
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func loginAs(e *env, ip, user, password string) *httptest.ResponseRecorder {
	return postAs(e, ip, "/api/v1/session", map[string]string{"username": user, "password": password})
}

// Sign-in failure lockout: failures from one address are counted in
// front of the password check. Below the limit a correct sign-in
// still works and clears the count; at the limit the address is
// refused — even with the correct password, which proves the refusal
// happens before any password check — while other addresses sign in
// as usual.
func TestLoginFailureLockout(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	// The request-rate buckets stay out of the way: this test is about
	// the failure count, not the rates (they have their own tests).
	e.srv.login.SetRate(1e6, 1e6)
	e.srv.loginAll.SetRate(1e6, 1e6)

	const ipA, ipB = "192.0.2.10", "192.0.2.11"
	wrong := func(ip string) int { return loginAs(e, ip, "admin", "wrong-password").Code }
	right := func(ip string) int { return loginAs(e, ip, "admin", "secret123").Code }

	// Four failures, then the correct password still signs in — and
	// the success clears the count, so the same happens again.
	for round := range 2 {
		for i := range loginMaxFails - 1 {
			if c := wrong(ipA); c != http.StatusUnauthorized {
				t.Fatalf("round %d failure %d: %d, want 401", round, i+1, c)
			}
		}
		if c := right(ipA); c != http.StatusOK {
			t.Fatalf("round %d: correct sign-in after %d failures: %d, want 200", round, loginMaxFails-1, c)
		}
	}

	// Five failures lock the address. The attempts themselves are
	// ordinary refusals; the next one is refused before the password
	// check, so even the correct password gets a 429.
	for i := range loginMaxFails {
		if c := wrong(ipA); c != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d, want 401", i+1, c)
		}
	}
	rec := loginAs(e, ipA, "admin", "secret123")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked address with the correct password: %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"code":"rate_limited"`) {
		t.Fatalf("lockout answer lacks the rate-limit shape: %v %s", rec.Header(), rec.Body)
	}
	// Setup sits behind the same lock (it is refused before its own
	// checks: an unlocked address hears "already done" instead).
	if rec := postAs(e, ipA, "/api/v1/setup", map[string]string{"username": "x", "password": "secret123"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("setup from a locked address: %d, want 429", rec.Code)
	}
	if rec := postAs(e, ipB, "/api/v1/setup", map[string]string{"username": "x", "password": "secret123"}); rec.Code != http.StatusConflict {
		t.Fatalf("setup from another address: %d, want 409", rec.Code)
	}
	// While A stays locked under a continued flood, B signs in.
	for range 3 {
		if c := wrong(ipA); c != http.StatusTooManyRequests {
			t.Fatalf("flood from a locked address: %d, want 429", c)
		}
	}
	if c := right(ipB); c != http.StatusOK {
		t.Fatalf("sign-in from another address during the lock: %d, want 200", c)
	}
	// Failures against an account that does not exist count the same:
	// the lock must not become a way to probe which names exist.
	const ipC = "192.0.2.12"
	for range loginMaxFails {
		if c := loginAs(e, ipC, "no-such-user", "wrong-password").Code; c != http.StatusUnauthorized {
			t.Fatalf("unknown account failure: %d, want 401", c)
		}
	}
	if c := loginAs(e, ipC, "no-such-user", "wrong-password").Code; c != http.StatusTooManyRequests {
		t.Fatalf("unknown account after the limit: %d, want 429", c)
	}
}
