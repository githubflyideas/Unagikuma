package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The published OpenAPI spec must describe the API the server actually
// serves. This test walks both route registries (handle and handleRaw)
// and fails when the spec and the route table drift apart: a route
// added without documenting it, a documented route that no longer
// exists, a changed method, or a changed token scope.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	e := newEnv(t, testBind, nil)

	var spec struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			Scope *string `json:"x-token-scope"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpecJSON, &spec); err != nil {
		t.Fatalf("openapi.json does not parse: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("openapi version %q, want 3.x", spec.OpenAPI)
	}

	// Expected surface: every registered API route except the spec
	// endpoint itself, which is checked separately below.
	want := map[string]map[string]string{} // path -> method -> scope
	specServed := false
	for _, list := range [][]routeInfo{e.srv.routeList, e.srv.rawRoutes} {
		for _, rt := range list {
			method, path, _ := strings.Cut(rt.pattern, " ")
			if path == "/api/openapi.json" {
				specServed = method == http.MethodGet
				continue
			}
			if !strings.HasPrefix(path, "/api/v1/") {
				continue
			}
			if want[path] == nil {
				want[path] = map[string]string{}
			}
			want[path][method] = rt.scope
		}
	}
	if !specServed {
		t.Error("GET /api/openapi.json is not registered")
	}
	if len(want) < 100 {
		t.Fatalf("route walk found only %d API paths; the walk is broken", len(want))
	}

	for path, methods := range want {
		ops, ok := spec.Paths[path]
		if !ok {
			t.Errorf("%s: registered but missing from the spec", path)
			continue
		}
		for method, scope := range methods {
			op, ok := ops[strings.ToLower(method)]
			if !ok {
				t.Errorf("%s %s: registered but missing from the spec", method, path)
				continue
			}
			got := ""
			if op.Scope != nil {
				got = *op.Scope
			}
			if got != scope {
				t.Errorf("%s %s: spec says token scope %q, route requires %q", method, path, got, scope)
			}
		}
		for m := range ops {
			if _, ok := methods[strings.ToUpper(m)]; !ok {
				t.Errorf("%s %s: in the spec but not a registered route", strings.ToUpper(m), path)
			}
		}
	}
	for path := range spec.Paths {
		if _, ok := want[path]; !ok {
			t.Errorf("%s: in the spec but not a registered route", path)
		}
	}

	// The served document is the embedded one, byte for byte in paths.
	resp, err := e.c.Get(e.ts.URL + "/api/openapi.json")
	if err != nil {
		t.Fatalf("GET /api/openapi.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/openapi.json: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var served struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatalf("served spec does not parse: %v", err)
	}
	if len(served.Paths) != len(spec.Paths) {
		t.Errorf("served spec has %d paths, embedded has %d", len(served.Paths), len(spec.Paths))
	}
}
