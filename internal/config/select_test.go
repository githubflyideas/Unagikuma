package config

import (
	"sort"
	"strings"
	"testing"
)

const selectSrc = `@ IN SOA master. ops.example.com. ( 1 1m 15s 5s 3 )
bj     IN NODE region=north
www    IN HTTP https://www.example.com/ title="Web site" group=public tags=site:bj,tier:1
api    IN HTTP http://10.0.0.5:8080/health group=public tags=site:sh
db     IN TCP  10.0.0.6:5432 title="Postgres" tags=site:bj
gw     IN PING 192.168.1.1 description="core router"
ntp    IN UDP  10.0.0.1:ntp
$SCOPE region:north
bjsw   IN PING 10.10.0.2
`

func selectNames(t *testing.T, q string) string {
	t.Helper()
	_, cfg, diags := Load("t", []byte(selectSrc), Options{})
	if HasErrors(diags) {
		t.Fatal(diags)
	}
	sel, err := ParseSelector(q)
	if err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	var out []string
	for _, m := range cfg.Monitors {
		if sel.Match(m) {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestSelector(t *testing.T) {
	for q, want := range map[string]string{
		"group:public":             "api,www",
		"tag:site:bj":              "db,www",
		"tag:site=bj -type:tcp":    "www",
		"tag:tier":                 "www",
		"cidr:10.0.0.0/24":         "api,db,ntp",
		"port:5432,8080":           "api,db",
		"port:443":                 "www",
		"host:www.example.com":     "www",
		"svc:ntp":                  "ntp",
		"area:region:north":        "bjsw",
		"node:bj":                  "api,bjsw,db,gw,ntp,www",
		"router":                   "gw",
		`title:"web site"`:         "www",
		"name:/^b/":                "bjsw",
		"-group:public -type:ping": "db,ntp",
		"":                         "",
		"https://www.example.com/": "www",
	} {
		if got := selectNames(t, q); got != want {
			t.Errorf("%q: got %q, want %q", q, got, want)
		}
	}
	for _, bad := range []string{"status:critical", "cidr:nope", "port:x", "name:/[/"} {
		if _, err := ParseSelector(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
