package notify

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Preset is a webhook template for a popular service: a form, a request
// template and a success rule — no code per service.
type Preset struct {
	Name        string            `json:"name"`
	Label       string            `json:"label"`
	Fields      []Field           `json:"fields"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	ContentType string            `json:"content_type"` // json | form | text
	Success     struct {
		Field  string `json:"field"`
		Equals string `json:"equals"`
	} `json:"success"`
}

//go:embed presets.json
var presetsJSON []byte

var presets = map[string]*Preset{}

func init() {
	var list []*Preset
	if err := json.Unmarshal(presetsJSON, &list); err != nil {
		panic("notify: presets.json: " + err.Error())
	}
	for _, p := range list {
		for _, src := range append([]string{p.URL, p.Body}, values(p.Headers)...) {
			if err := ValidateTemplate(src); err != nil {
				panic(fmt.Sprintf("notify: preset %s: %v", p.Name, err))
			}
		}
		presets[p.Name] = p
	}
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (s *Sender) buildPreset(ctx context.Context, p *Preset, opts map[string]string, m *Message) (*http.Request, checkFunc, error) {
	o := map[string]string{}
	for _, f := range p.Fields {
		if f.Default != "" {
			o[f.Key] = f.Default
		}
	}
	for k, v := range opts {
		o[k] = v
	}
	for _, f := range p.Fields {
		if f.Type == "url" && o[f.Key] != "" {
			o[f.Key] = strings.TrimRight(o[f.Key], "/")
		}
	}
	plain := templateData(m, o, false)
	u, err := renderTemplate(p.URL, plain)
	if err != nil {
		return nil, nil, err
	}
	body, err := renderTemplate(p.Body, templateData(m, o, p.ContentType == "json"))
	if err != nil {
		return nil, nil, err
	}
	ct := map[string]string{"json": ctJSON, "form": "application/x-www-form-urlencoded", "text": "text/plain; charset=utf-8"}[p.ContentType]
	req, err := s.newRequest(ctx, p.Method, u, ct, []byte(body), m)
	if err != nil {
		return nil, nil, err
	}
	for name, tpl := range p.Headers {
		v, err := renderTemplate(tpl, plain)
		if err != nil {
			return nil, nil, err
		}
		// Header values must be single-line: drop newlines from titles.
		if v = strings.Join(strings.Fields(v), " "); v != "" {
			req.Header.Set(name, v)
		}
	}
	if p.Success.Field == "" {
		return req, status2xx, nil
	}
	return req, jsonCode(p.Success.Equals, p.Success.Field), nil
}

// Presets returns the preset definitions.
func Presets() []*Preset {
	out := make([]*Preset, 0, len(presets))
	for _, p := range presets {
		out = append(out, p)
	}
	return out
}
