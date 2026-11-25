package importer

import (
	"strings"
	"testing"
)

// The shapes real Gatus files use: several conditions, body and JSON
// path conditions, response-time conditions, icmp/tcp/DNS endpoints,
// query strings, fields with no equivalent, YAML anchors and merges.
// What maps is imported; what does not map is in the report.
func TestGatusRealShapes(t *testing.T) {
	data := []byte(`
endpoints:
  - name: web
    url: "https://example.invalid/health?deep=1"
    interval: 1m
    conditions:
      - "[STATUS] == 200"
      - "[BODY].status == UP"
  - name: range
    url: "https://example.invalid/"
    conditions:
      - "[STATUS] < 400"
      - "[RESPONSE_TIME] < 300"
  - name: gw
    url: "icmp://10.0.0.1"
    conditions:
      - "[CONNECTED] == true"
  - name: db
    url: "tcp://db.example.invalid:5432"
    conditions:
      - "[CONNECTED] == true"
  - name: dns
    url: "10.2.0.100"
    dns:
      query-name: pihole.example.com
      query-type: A
    conditions:
      - "[DNS_RCODE] == NOERROR"
      - "[BODY] == 192.0.2.16"
  - name: cert
    url: "https://example.invalid/"
    conditions:
      - "[STATUS] == 200"
      - "[CERTIFICATE_EXPIRATION] > 240h"
  - name: env
    url: "${APP_BASE_URL}/health"
    conditions:
      - "[STATUS] == 200"
  - name: down-is-up
    url: "icmp://10.0.0.2"
    conditions:
      - "[CONNECTED] == false"
`)
	r, err := Gatus(data, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Monitors != 6 {
		t.Fatalf("monitors %d:\n%s", r.Monitors, r.Report(ReportOptions{}))
	}
	if r.Blocked || r.ActivationError() != nil {
		t.Fatal("a partial import must apply")
	}
	byName := map[string]Item{}
	for _, it := range r.Items {
		byName[it.Source] = it
	}
	if it := byName["web"]; it.Status != StatusImported || !strings.Contains(r.Text, "json-path=status") || !strings.Contains(r.Text, "json-expect=UP") {
		t.Fatalf("web: %+v\n%s", it, r.Text)
	}
	if it := byName["range"]; it.Status != StatusAttention || !strings.Contains(r.Text, "expect=100-399") || !strings.Contains(it.Why, "RESPONSE_TIME") {
		t.Fatalf("range: %+v", it)
	}
	if it := byName["gw"]; it.Status != StatusImported || it.Type != "Ping" {
		t.Fatalf("gw: %+v", it)
	}
	if it := byName["db"]; it.Status != StatusImported || it.Type != "TCP port" {
		t.Fatalf("db: %+v", it)
	}
	if it := byName["dns"]; it.Status != StatusImported || it.Type != "DNS" ||
		!strings.Contains(r.Text, "query=pihole.example.com") || !strings.Contains(r.Text, "expect=192.0.2.16") {
		t.Fatalf("dns: %+v\n%s", it, r.Text)
	}
	if it := byName["cert"]; it.Status != StatusImported || !strings.Contains(r.Text, "cert-days=10") {
		t.Fatalf("cert: %+v\n%s", it, r.Text)
	}
	if it := byName["env"]; it.Status != StatusLeftOut || !strings.Contains(it.Why, "placeholder") {
		t.Fatalf("env: %+v", it)
	}
	if it := byName["down-is-up"]; it.Status != StatusLeftOut || !strings.Contains(it.Why, "upside-down") {
		t.Fatalf("down-is-up: %+v", it)
	}
}

// An endpoint that cannot be imported lists every reason, not just
// the first one that applied.
func TestGatusAllReasons(t *testing.T) {
	r, err := Gatus([]byte(`
endpoints:
  - name: bad
    url: "ssh://example.invalid"
    conditions:
      - "[BODY] == never"
      - "[WAT] == 1"
`), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	it := r.Items[0]
	if it.Status != StatusLeftOut || !strings.Contains(it.Why, "scheme") || !strings.Contains(it.Why, "unsupported condition") {
		t.Fatalf("reasons masked: %+v", it)
	}
}

// Endpoint fields with no equivalent (alerts, ui, anything unknown)
// are ignored with a note; the endpoint is imported.
func TestGatusIgnoredFieldsAreNotes(t *testing.T) {
	r, err := Gatus([]byte(`
endpoints:
  - name: noted
    url: https://example.invalid
    conditions: ['[STATUS] == 200']
    ui: {hide-url: true}
    graphql: true
    alerts:
      - type: email
`), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	it := r.Items[0]
	if it.Status != StatusAttention || !strings.Contains(it.Why, "alerts") || !strings.Contains(it.Why, "ui") || !strings.Contains(it.Why, "graphql") {
		t.Fatalf("ignored fields not all reported: %+v", it)
	}
}

// Anchors, aliases and merge keys are read, the way real files use
// them (a shared block merged into endpoints, an anchored block used
// twice).
func TestGatusAnchorsAliasesMerges(t *testing.T) {
	r, err := Gatus([]byte(`
shared: &shared
  interval: 45s
  conditions: ["[STATUS] == 200"]
one: &first
  name: first
  url: https://example.invalid/1
  conditions: ["[STATUS] == 200"]
endpoints:
  - <<: *shared
    name: merged
    url: https://example.invalid/2
  - *first
`), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Monitors != 2 || !strings.Contains(r.Text, "45s") {
		t.Fatalf("anchors/merges: %d monitors\n%s", r.Monitors, r.Text)
	}
	// An alias of a node still being defined is a cycle, not a value.
	if _, err := Gatus([]byte("endpoints: &a [*a]"), nil, Options{}); err == nil {
		t.Fatal("self-referential alias accepted")
	}
	if _, err := Gatus([]byte("endpoints:\n  - name: x\n    url: *missing\n"), nil, Options{}); err == nil {
		t.Fatal("unknown alias accepted")
	}
}

// Checkmate's own defaults — proxyMode inherit and a status window of
// (5, 60) — import, with the approximations in the report. Port and
// ping monitors map to TCP and ping checks.
func TestCheckmateVendorDefaults(t *testing.T) {
	data := []byte(`[
 {"name":"site","type":"http","url":"https://example.invalid","interval":20000,"isActive":true,"proxyMode":"inherit","statusWindowSize":5,"statusWindowThreshold":60},
 {"name":"db port","type":"port","url":"mongodb","port":27017,"interval":20000,"isActive":true,"proxyMode":"inherit","statusWindowSize":5,"statusWindowThreshold":60},
 {"name":"router","type":"ping","url":"127.0.0.1","interval":20000,"isActive":true,"proxyMode":"inherit","statusWindowSize":10,"statusWindowThreshold":50},
 {"name":"agent","type":"hardware","url":"http://example.invalid/metrics","interval":20000,"isActive":true,"proxyMode":"inherit","statusWindowSize":5,"statusWindowThreshold":60}
]`)
	r, err := Checkmate(data, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Monitors != 3 || r.Blocked {
		t.Fatalf("monitors %d blocked %v:\n%s", r.Monitors, r.Blocked, r.Report(ReportOptions{}))
	}
	if !strings.Contains(r.Text, "mongodb:27017") {
		t.Fatalf("port target not built from url + port:\n%s", r.Text)
	}
	byName := map[string]Item{}
	for _, it := range r.Items {
		byName[it.Source] = it
	}
	// (5, 60) -> attempts = ceil(5*60/100) = 3, said in the report.
	if it := byName["site"]; it.Status != StatusAttention || !strings.Contains(it.Why, "attempts=3") || !strings.Contains(it.Why, "proxy") {
		t.Fatalf("site: %+v", it)
	}
	if !strings.Contains(r.Text, "attempts=3") || !strings.Contains(r.Text, "attempts=5") {
		t.Fatalf("window mapping missing from records:\n%s", r.Text)
	}
	if it := byName["router"]; it.Type != "Ping" {
		t.Fatalf("router: %+v", it)
	}
	if it := byName["agent"]; it.Status != StatusLeftOut || !strings.Contains(it.Why, "monitor type") {
		t.Fatalf("agent: %+v", it)
	}
}
