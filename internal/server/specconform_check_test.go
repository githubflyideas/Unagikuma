package server

import (
	"encoding/json"
	"fmt"
	"mime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The OpenAPI document, parsed once for checkAnswer.
var specDoc = sync.OnceValue(func() map[string]any {
	var d map[string]any
	if err := json.Unmarshal(openAPISpecJSON, &d); err != nil {
		return nil
	}
	return d
})

// checkAnswer says what is wrong with an answer the server gave, judged by
// the OpenAPI document: the status must be documented for the operation,
// and a JSON body must match that status's schema strictly (no property
// the document does not name). "" means it conforms; paths the document
// does not cover (the cluster protocol) are not judged.
func checkAnswer(method, path string, code int, ctype string, body []byte) string {
	d := specDoc()
	if d == nil {
		return "openapi.json does not parse"
	}
	ops, ok := d["paths"].(map[string]any)[path].(map[string]any)
	if !ok {
		if strings.HasPrefix(path, "/api/v1/agent/") || !strings.HasPrefix(path, "/api/v1/") {
			return ""
		}
		return "path not in the document"
	}
	op, ok := ops[strings.ToLower(method)].(map[string]any)
	if !ok {
		return "operation not in the document"
	}
	resp, ok := op["responses"].(map[string]any)[strconv.Itoa(code)].(map[string]any)
	if !ok {
		return fmt.Sprintf("status %d is not documented", code)
	}
	content, _ := resp["content"].(map[string]any)
	if code == 204 || len(body) == 0 {
		if code == 204 && len(content) > 0 {
			return "204 documented with a body"
		}
		if len(body) == 0 {
			return ""
		}
	}
	mt, _, _ := mime.ParseMediaType(ctype)
	media, ok := content[mt].(map[string]any)
	if !ok {
		names := make([]string, 0, len(content))
		for k := range content {
			names = append(names, k)
		}
		sort.Strings(names)
		return fmt.Sprintf("content type %q is not documented (documented: %s)", mt, strings.Join(names, ", "))
	}
	if !strings.Contains(mt, "json") {
		return ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "body is not JSON: " + err.Error()
	}
	schema, _ := media["schema"].(map[string]any)
	if errs := (&specChecker{doc: d}).check(schema, v, "$", 0); len(errs) > 0 {
		if len(errs) > 5 {
			errs = append(errs[:5], fmt.Sprintf("and %d more", len(errs)-5))
		}
		return strings.Join(errs, "; ")
	}
	return ""
}

type specChecker struct{ doc map[string]any }

func (c *specChecker) resolve(s map[string]any) map[string]any {
	for i := 0; i < 10; i++ {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s
		}
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		s, _ = c.doc["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
		if s == nil {
			return map[string]any{"x-missing": ref}
		}
	}
	return s
}

func (c *specChecker) check(s map[string]any, v any, at string, depth int) []string {
	if s == nil {
		return []string{at + ": no schema"}
	}
	if depth > 40 {
		return nil
	}
	s = c.resolve(s)
	if ref, ok := s["x-missing"].(string); ok {
		return []string{at + ": unknown " + ref}
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if alts, ok := s[k].([]any); ok {
			matched := 0
			var first []string
			for _, a := range alts {
				e := c.check(a.(map[string]any), v, at, depth+1)
				if len(e) == 0 {
					matched++
				} else if first == nil {
					first = e
				}
			}
			if matched == 0 || (k == "oneOf" && matched > 1) {
				return []string{fmt.Sprintf("%s: matches %d of the %s alternatives (%s)", at, matched, k, strings.Join(first, "; "))}
			}
			return nil
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		var errs []string
		for _, a := range all {
			errs = append(errs, c.check(a.(map[string]any), v, at, depth+1)...)
		}
		return errs
	}
	var types []string
	switch t := s["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, x := range t {
			types = append(types, x.(string))
		}
	}
	if len(types) == 0 {
		if _, obj := s["properties"]; !obj {
			if len(s) == 0 || s["additionalProperties"] == nil {
				return []string{at + ": the document does not say what this is"}
			}
		}
		types = []string{"object"}
	}
	if v == nil {
		for _, t := range types {
			if t == "null" {
				return nil
			}
		}
		return []string{at + ": null is not allowed"}
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if e == v {
				found = true
			}
		}
		if !found {
			return []string{fmt.Sprintf("%s: %v is not one of %v", at, v, enum)}
		}
	}
	var errs []string
	matchedType := false
	for _, t := range types {
		switch t {
		case "object":
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			matchedType = true
			props, _ := s["properties"].(map[string]any)
			if req, ok := s["required"].([]any); ok {
				for _, r := range req {
					if _, ok := m[r.(string)]; !ok {
						errs = append(errs, at+": missing "+r.(string))
					}
				}
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if p, ok := props[k].(map[string]any); ok {
					errs = append(errs, c.check(p, m[k], at+"."+k, depth+1)...)
					continue
				}
				switch ap := s["additionalProperties"].(type) {
				case map[string]any:
					errs = append(errs, c.check(ap, m[k], at+"."+k, depth+1)...)
				case bool:
					if !ap {
						errs = append(errs, at+": undocumented property "+k)
					}
				default:
					errs = append(errs, at+": undocumented property "+k)
				}
			}
		case "array":
			l, ok := v.([]any)
			if !ok {
				continue
			}
			matchedType = true
			if n, ok := s["minItems"].(float64); ok && float64(len(l)) < n {
				errs = append(errs, fmt.Sprintf("%s: fewer than %v items", at, n))
			}
			if n, ok := s["maxItems"].(float64); ok && float64(len(l)) > n {
				errs = append(errs, fmt.Sprintf("%s: more than %v items", at, n))
			}
			items, _ := s["items"].(map[string]any)
			for i, x := range l {
				errs = append(errs, c.check(items, x, fmt.Sprintf("%s[%d]", at, i), depth+1)...)
				if len(errs) > 10 {
					break
				}
			}
		case "string":
			str, ok := v.(string)
			if !ok {
				continue
			}
			matchedType = true
			if s["format"] == "date-time" {
				if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %q is not an RFC 3339 time", at, str))
				}
			}
		case "integer":
			if f, ok := v.(float64); ok && f == float64(int64(f)) {
				matchedType = true
			}
		case "number":
			if _, ok := v.(float64); ok {
				matchedType = true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				matchedType = true
			}
		}
	}
	if !matchedType {
		return []string{fmt.Sprintf("%s: %T is not %s", at, v, strings.Join(types, " or "))}
	}
	return errs
}
