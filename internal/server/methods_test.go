package server

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestEveryWrongMethodIsAJSON405: at every API address, a method no
// endpoint there takes answers 405 in the error envelope with code
// method_not_allowed and an Allow header, and every method the Allow
// header names is one that address does take.
func TestEveryWrongMethodIsAJSON405(t *testing.T) {
	e := newEnv(t, testBind, nil)
	h := e.srv.Handler()
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "192.0.2.1:5000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	seen := map[string]bool{}
	checked := 0
	for _, ri := range e.srv.routeList {
		_, path, _ := strings.Cut(ri.pattern, " ")
		path = pathParam.ReplaceAllString(path, "x")
		if seen[path] {
			continue
		}
		seen[path] = true
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE"} {
			rec := call(m, path)
			if rec.Code != 405 {
				continue
			}
			checked++
			var body struct{ Code, Message string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != CodeMethodNotAllowed || body.Message == "" {
				t.Errorf("%s %s: 405 body %q", m, path, rec.Body.String())
			}
			allow := rec.Header().Get("Allow")
			if allow == "" {
				t.Errorf("%s %s: 405 without Allow", m, path)
			}
			for _, a := range strings.Split(allow, ", ") {
				if a == "HEAD" {
					continue
				}
				if c := call(a, path).Code; c == 405 {
					t.Errorf("%s %s: Allow names %s, which is refused too", m, path, a)
				}
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d wrong-method answers checked", checked)
	}
	// An address no endpoint has is a JSON 404.
	rec := call("POST", "/api/v1/no_such_thing")
	var body struct{ Code string }
	if rec.Code != 404 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Code != CodeNotFound {
		t.Fatalf("unknown address: %d %s", rec.Code, rec.Body.String())
	}
}
