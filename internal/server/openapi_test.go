package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// openAPIDoc is the part of the document the tests below read.
type openAPIDoc struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Version string `json:"version"`
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas         map[string]json.RawMessage `json:"schemas"`
		SecuritySchemes map[string]struct {
			In   string `json:"in"`
			Name string `json:"name"`
		} `json:"securitySchemes"`
	} `json:"components"`
}

func loadOpenAPI(t *testing.T) openAPIDoc {
	t.Helper()
	var d openAPIDoc
	if err := json.Unmarshal(openAPISpecJSON, &d); err != nil {
		t.Fatalf("openapi.json does not parse: %v", err)
	}
	return d
}

// The published OpenAPI spec must describe the API the server actually
// serves. This test walks both route registries (handle and handleRaw)
// and fails when the spec and the route table drift apart: a route
// added without documenting it, a documented route that no longer
// exists, a changed method, or a changed token scope.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	e := newEnv(t, testBind, nil)
	spec := loadOpenAPI(t)
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("openapi version %q, want 3.x", spec.OpenAPI)
	}

	want := map[string]map[string]string{} // path -> method -> scope
	for _, list := range [][]routeInfo{e.srv.routeList, e.srv.rawRoutes} {
		for _, rt := range list {
			method, path, _ := strings.Cut(rt.pattern, " ")
			if want[path] == nil {
				want[path] = map[string]string{}
			}
			want[path][method] = rt.scope
		}
	}
	if len(want) < 100 {
		t.Fatalf("route walk found only %d API paths; the walk is broken", len(want))
	}
	for path := range want {
		if !strings.HasPrefix(path, "/api/v1/") && path != "/api/push/{token}" && path != "/push/{token}" &&
			path != "/healthz" && path != "/metrics" && !strings.HasPrefix(path, "/badge/") && !strings.HasPrefix(path, "/status/") {
			t.Errorf("%s: an API route outside /api/v1", path)
		}
	}

	for path, methods := range want {
		ops, ok := spec.Paths[path]
		if !ok {
			t.Errorf("%s: registered but missing from the spec", path)
			continue
		}
		for method, scope := range methods {
			raw, ok := ops[strings.ToLower(method)]
			if !ok {
				t.Errorf("%s %s: registered but missing from the spec", method, path)
				continue
			}
			var op struct {
				Scope *string `json:"x-token-scope"`
			}
			_ = json.Unmarshal(raw, &op)
			got := ""
			if op.Scope != nil {
				got = *op.Scope
			}
			if got != scope {
				t.Errorf("%s %s: spec says token scope %q, route requires %q", method, path, got, scope)
			}
		}
		for m := range ops {
			if m == "parameters" {
				continue
			}
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

	// The served document is the embedded one, byte for byte.
	resp, err := e.c.Get(e.ts.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("GET /api/v1/openapi.json: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != string(openAPISpecJSON) {
		t.Fatalf("GET /api/v1/openapi.json: %d, %d bytes (embedded %d)", resp.StatusCode, len(body), len(openAPISpecJSON))
	}
}

// The document's version is the release it ships in: the newest version
// in CHANGELOG.md.
func TestOpenAPIVersionIsTheRelease(t *testing.T) {
	spec := loadOpenAPI(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^## \[?v?(\d+\.\d+\.\d+)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no version heading in CHANGELOG.md")
	}
	if spec.Info.Version != string(m[1]) {
		t.Errorf("openapi info.version %q, CHANGELOG's newest release %q", spec.Info.Version, m[1])
	}
}

// The session cookie the document names is the one the server sets.
func TestOpenAPISessionCookie(t *testing.T) {
	spec := loadOpenAPI(t)
	sc := spec.Components.SecuritySchemes["sessionCookie"]
	if sc.In != "cookie" || sc.Name != sessionCookiePrefix+"_<installation id>" {
		t.Errorf("session cookie in the document: %+v, the server uses %s_<installation id>", sc, sessionCookiePrefix)
	}
}

// Every error code the server can answer is in the Error schema's enum,
// and the enum names no code the server never uses. The codes are the
// Code* constants of this package.
func TestOpenAPIErrorCodes(t *testing.T) {
	spec := loadOpenAPI(t)
	var es struct {
		Properties struct {
			Code struct {
				Enum []string `json:"enum"`
			} `json:"code"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Components.Schemas["Error"], &es); err != nil {
		t.Fatal(err)
	}
	doc := map[string]bool{}
	for _, c := range es.Properties.Code.Enum {
		doc[c] = true
	}
	code := map[string]bool{}
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					code[v] = true
				}
			}
			return true
		})
	}
	if len(code) < 10 {
		t.Fatalf("found only %d Code constants; the scan is broken", len(code))
	}
	var missing, extra []string
	for c := range code {
		if !doc[c] {
			missing = append(missing, c)
		}
	}
	for c := range doc {
		if !code[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("Error.code enum: missing %v, never used %v", missing, extra)
	}
}

// Every success answer is described: a JSON body has a schema that names
// its fields (checkAnswer then holds every test's answers to it), and a
// DELETE answers 204 with no body.
func TestOpenAPISuccessSchemas(t *testing.T) {
	spec := loadOpenAPI(t)
	var d map[string]any
	_ = json.Unmarshal(openAPISpecJSON, &d)
	c := &specChecker{doc: d}
	for path, ops := range spec.Paths {
		for method, raw := range ops {
			var op struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema map[string]any `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
			}
			_ = json.Unmarshal(raw, &op)
			ok2xx := false
			for st, r := range op.Responses {
				if !strings.HasPrefix(st, "2") {
					continue
				}
				ok2xx = true
				if method == "delete" && (st != "204" || len(r.Content) > 0) {
					t.Errorf("DELETE %s: documents %s with %d content types, want 204 without a body", path, st, len(r.Content))
				}
				for ct, m := range r.Content {
					if strings.Contains(ct, "json") && vague(c, m.Schema, 0) {
						t.Errorf("%s %s %s: the schema does not say what the answer holds", strings.ToUpper(method), path, st)
					}
				}
			}
			if !ok2xx {
				t.Errorf("%s %s: no success answer documented", strings.ToUpper(method), path)
			}
			if _, ok := op.Responses["405"]; !ok {
				t.Errorf("%s %s: 405 not documented", strings.ToUpper(method), path)
			}
		}
	}
}

// vague: the schema, or a part of it, is empty or an object with no
// properties (a deliberate free-form object says so in its description).
func vague(c *specChecker, s map[string]any, depth int) bool {
	if depth > 30 {
		return false
	}
	s = c.resolve(s)
	if len(s) == 0 {
		return true
	}
	for _, k := range []string{"oneOf", "anyOf", "allOf"} {
		if alts, ok := s[k].([]any); ok {
			for _, a := range alts {
				if vague(c, a.(map[string]any), depth+1) {
					return true
				}
			}
			return false
		}
	}
	props, _ := s["properties"].(map[string]any)
	for _, p := range props {
		if vague(c, p.(map[string]any), depth+1) {
			return true
		}
	}
	switch ap := s["additionalProperties"].(type) {
	case map[string]any:
		return vague(c, ap, depth+1)
	case bool:
		if ap && len(props) == 0 && s["description"] == nil {
			return true
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		return vague(c, items, depth+1)
	}
	_, typed := s["type"]
	return !typed && len(props) == 0
}
