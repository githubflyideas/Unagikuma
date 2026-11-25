package notify

import (
	"net/url"
	"strings"
	"testing"
)

// A webhook with content-type=form and a custom body template must deliver
// substituted values intact: a value holding &, =, + or % is one form
// field's value, not new fields. Regression test for the form-escaping
// defect found 2026-11-12 (values were substituted raw).
func TestWebhookFormTemplateEscaping(t *testing.T) {
	cap := &capture{}
	srv := cap.server(t)
	m := problem()
	m.Title = `R&D "core" = A+B 100%`
	m.Monitor = "a&b=c"
	m.Detail = `x&y=z + 100% sure`
	opts := map[string]string{
		"url": srv.URL, "content-type": "form",
		"body": "name={{.Name}}&monitor={{.Monitor}}&detail={{.Detail}}&n={{.Count}}",
	}
	if a := send(t, "webhook", opts, m); !a.OK {
		t.Fatal(a)
	}
	req, body := cap.last()
	if ct := req.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Fatalf("content type %q", ct)
	}
	vals, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("body does not parse as a form: %v\n%s", err, body)
	}
	if got := vals.Get("name"); got != m.Title {
		t.Errorf("name = %q, want %q", got, m.Title)
	}
	if got := vals.Get("monitor"); got != "a&b=c" {
		t.Errorf("monitor = %q, want %q", got, "a&b=c")
	}
	if got := vals.Get("detail"); got != m.Detail {
		t.Errorf("detail = %q, want %q", got, m.Detail)
	}
	if got := vals.Get("n"); got != "1" {
		t.Errorf("n = %q, want 1", got)
	}
	if len(vals) != 4 {
		t.Errorf("stray fields in %v (body %s)", vals, body)
	}
	// {{raw .X}} opts out of the escaping, as it does for JSON.
	opts["body"] = "name={{raw .Name}}"
	if a := send(t, "webhook", opts, m); !a.OK {
		t.Fatal(a)
	}
	_, body = cap.last()
	if !strings.Contains(string(body), "name="+m.Title) {
		t.Errorf("raw value not verbatim: %s", body)
	}
	// A literal % in a value must not corrupt neighbouring fields either.
	opts["body"] = "a={{.Name}}&b=after"
	if a := send(t, "webhook", opts, m); !a.OK {
		t.Fatal(a)
	}
	_, body = cap.last()
	vals, err = url.ParseQuery(string(body))
	if err != nil || vals.Get("a") != m.Title || vals.Get("b") != "after" {
		t.Errorf("percent value: %v %v (body %s)", vals, err, body)
	}
}
