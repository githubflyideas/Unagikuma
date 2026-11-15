package probe

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func appServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nginx_status":
			fmt.Fprint(w, "Active connections: 291 \nserver accepts handled requests\n 16630948 16630940 31070465 \nReading: 6 Writing: 179 Waiting: 106 \n")
		case "/metrics":
			fmt.Fprint(w, "# TYPE haproxy_backend_up gauge\nhaproxy_backend_up{proxy=\"a\"} 1\nhaproxy_backend_up{proxy=\"b\"} 0\nhaproxy_frontend_current_sessions{proxy=\"a\"} 5\n"+
				"haproxy_backend_current_queue{proxy=\"a\"} 0\nhaproxy_backend_active_servers{proxy=\"a\"} 2\nhaproxy_frontend_http_requests_total{proxy=\"a\"} 10\n"+
				"haproxy_frontend_http_responses_total{proxy=\"a\",code=\"5xx\"} 1\nhaproxy_backend_connection_errors_total{proxy=\"a\"} 0\n")
		case "/other":
			fmt.Fprint(w, "<html>welcome</html>")
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAppCheck(t *testing.T) {
	srv := appServer(t)
	spec := func(path string, o map[string]string) Spec {
		return Spec{Type: "HTTP", Target: srv.URL + path, Opts: o}
	}
	r := run(t, spec("/nginx_status", map[string]string{"app": "nginx", "warn": "200"}))
	want(t, r, VoteOK, ReasonOK)
	if r.Metrics["value"] != 291 || r.Metrics["app.reading"] != 6 || r.Metrics["value.counter"] != 0 {
		t.Fatalf("metrics %v", r.Metrics)
	}
	if !strings.Contains(r.Detail, "Active connections 291") {
		t.Fatalf("detail %q", r.Detail)
	}
	// A counter field is flagged, so its rate is what the rules judge.
	r = run(t, spec("/nginx_status", map[string]string{"app": "nginx", "field": "requests", "warn": "1000"}))
	if r.Metrics["value"] != 31070465 || r.Metrics["value.counter"] != 1 {
		t.Fatalf("counter field %v", r.Metrics)
	}
	// Not that application's page: it fails and says so.
	r = run(t, spec("/other", map[string]string{"app": "nginx"}))
	want(t, r, VoteFail, ReasonAssertFailed)
	if !strings.Contains(r.Detail, "not the Nginx status page") {
		t.Fatalf("detail %q", r.Detail)
	}
	// A status error page is an HTTP failure, as for any HTTP check.
	want(t, run(t, spec("/missing", map[string]string{"app": "nginx"})), VoteFail, ReasonHTTPStatus)
	// A metrics page, narrowed to one series.
	r = run(t, spec("/metrics", map[string]string{"app": "haproxy", "field": "backend_up", "crit": "<1"}))
	if r.Vote != VoteOK || r.Metrics["value"] != 0 {
		t.Fatalf("haproxy %+v", r)
	}
	r = run(t, spec("/metrics", map[string]string{"app": "haproxy", "select": "proxy=a", "field": "backend_up"}))
	if r.Metrics["value"] != 1 {
		t.Fatalf("select %v", r.Metrics)
	}
	// Narrowed to a proxy that has no such field.
	r = run(t, spec("/metrics", map[string]string{"app": "haproxy", "select": "proxy=zz"}))
	want(t, r, VoteFail, ReasonAssertFailed)
}

func TestAppValidate(t *testing.T) {
	for _, c := range []struct {
		o  map[string]string
		ok bool
	}{
		{map[string]string{"app": "nginx"}, true},
		{map[string]string{"app": "nginx", "field": "waiting", "warn": "100", "crit": "200"}, true},
		{map[string]string{"app": "nope"}, false},
		{map[string]string{"app": "nginx", "field": "nope"}, false},
		{map[string]string{"field": "active"}, false},                        // field= without app=
		{map[string]string{"app": "nginx", "select": "proxy=a"}, false},      // a text page has no labels
		{map[string]string{"app": "haproxy", "select": "proxy"}, false},      // not label=value
		{map[string]string{"app": "nginx", "json-path": "$.x"}, false},       // two ways to read the number
		{map[string]string{"app": "nginx", "keyword": "x"}, false},           // the page is read by the profile
		{map[string]string{"app": "nginx", "warn": "500Mbps"}, true},         // a plain count takes any limit
		{map[string]string{"app": "nginx", "crit": "5", "warn": "9"}, false}, // crit nearer than warn
		{map[string]string{"warn": "5"}, false},                              // nothing to judge
	} {
		err := Validate(Spec{Type: "HTTP", Target: "http://127.0.0.1/x", Opts: c.o})
		if (err == nil) != c.ok {
			t.Errorf("%v: ok=%v, err=%v", c.o, c.ok, err)
		}
	}
	if got := OptionsFor("APP"); len(got) < 10 {
		t.Errorf("options for APP: %v", got)
	}
}
