package probe

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// pathStep is one step of a JSONPath subset: .name or [index].
type pathStep struct {
	key   string
	index int
	isIdx bool
}

// parseJSONPath parses "$.a.b[0].c" (also without the leading "$", and
// ["quoted key"] for keys with dots).
func parseJSONPath(p string) ([]pathStep, error) {
	s := strings.TrimSpace(p)
	s = strings.TrimPrefix(s, "$")
	var out []pathStep
	for len(s) > 0 {
		switch s[0] {
		case '.':
			s = s[1:]
			end := strings.IndexAny(s, ".[")
			if end < 0 {
				end = len(s)
			}
			if end == 0 {
				return nil, fmt.Errorf("json-path: empty key in %q", p)
			}
			out = append(out, pathStep{key: s[:end]})
			s = s[end:]
		case '[':
			end := strings.IndexByte(s, ']')
			if end < 0 {
				return nil, fmt.Errorf("json-path: missing ] in %q", p)
			}
			in := s[1:end]
			s = s[end+1:]
			if strings.HasPrefix(in, `"`) || strings.HasPrefix(in, "'") {
				out = append(out, pathStep{key: strings.Trim(in, `"'`)})
				continue
			}
			n, err := strconv.Atoi(in)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("json-path: bad index [%s]", in)
			}
			out = append(out, pathStep{index: n, isIdx: true})
		default:
			if len(out) == 0 {
				// "a.b" without "$.".
				s = "." + s
				continue
			}
			return nil, fmt.Errorf("json-path: unexpected %q", s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("json-path: empty path")
	}
	return out, nil
}

// checkJSON evaluates the json-path assertion. Without json-expect the
// path just has to exist.
func checkJSON(data []byte, path string, o opts) (string, bool) {
	steps, err := parseJSONPath(path)
	if err != nil {
		return err.Error(), false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "response is not valid JSON", false
	}
	for _, st := range steps {
		switch cur := v.(type) {
		case map[string]any:
			if st.isIdx {
				return fmt.Sprintf("%s: expected an array", path), false
			}
			next, ok := cur[st.key]
			if !ok {
				return fmt.Sprintf("%s: key %q not found", path, st.key), false
			}
			v = next
		case []any:
			if !st.isIdx || st.index >= len(cur) {
				return fmt.Sprintf("%s: index out of range", path), false
			}
			v = cur[st.index]
		default:
			return fmt.Sprintf("%s: cannot descend into a %T", path, v), false
		}
	}
	got := jsonText(v)
	if want, ok := o["json-expect"]; ok && got != want {
		return fmt.Sprintf("%s is %q, expected %q", path, got, want), false
	}
	if expr, ok := o["json-expect-num"]; ok {
		ne, _ := parseNumExpect(expr)
		f, err := strconv.ParseFloat(got, 64)
		if err != nil {
			return fmt.Sprintf("%s is %q, not a number", path, got), false
		}
		if !ne.match(f) {
			return fmt.Sprintf("%s is %s, expected %s", path, got, ne), false
		}
	}
	return "", true
}

// jsonText renders a JSON value as the text users compare against.
func jsonText(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
