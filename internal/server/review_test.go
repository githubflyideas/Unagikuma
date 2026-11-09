package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Status pages select with configuration selectors: tags, CIDRs, areas.
func TestStatusPageSelectors(t *testing.T) {
	e := newEnv(t, `@ IN SOA unagikuma. admin.localhost. ( 2026102801 1m 15s 5s 2 )
bj  IN NODE region=north
web IN HTTP http://192.168.1.10/ tags=site:bj
db  IN TCP 10.0.0.5:5432
$SCOPE region:north
core IN PING 10.10.0.1
$SCOPE *
bjpage  IN STATUSPAGE "BJ" select="tag:site:bj"
lan     IN STATUSPAGE "LAN" select="cidr:10.0.0.0/8"
north   IN STATUSPAGE "North" select="area:region:north"
`, nil)
	e.setup()
	for page, want := range map[string]int{"bjpage": 1, "lan": 2, "north": 1} {
		var sp struct {
			Monitors []struct{ Title string } `json:"monitors"`
		}
		if c := e.do("GET", "/api/v1/status/"+page, nil, &sp); c != 200 || len(sp.Monitors) != want {
			t.Errorf("%s: %d monitors, want %d (%d)", page, len(sp.Monitors), want, c)
		}
	}
}

// A URL with a query string survives being written into monitors.conf.
func TestURLWithQuery(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	rec := map[string]any{"record": map[string]any{"name": "q", "type": "HTTP", "target": "http://192.168.1.10/health?a=b&c=d",
		"opts": map[string]string{"keyword": "x=y"}}}
	if c := e.do("POST", "/api/v1/monitors", rec, nil); c != 200 {
		t.Fatalf("create: %d", c)
	}
	m := e.app.Config().Config.FindMonitor("q")
	if m == nil || m.Target != "http://192.168.1.10/health?a=b&c=d" || m.Opts["keyword"] != "x=y" {
		t.Fatalf("read back: %+v", m)
	}
}

func TestHealthzAndMetrics(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	anon := &env{t: t, app: e.app, srv: e.srv, ts: e.ts, c: e.ts.Client()}
	var h struct{ Status string }
	if c := anon.do("GET", "/healthz", nil, &h); c != 200 || h.Status != "ok" {
		t.Fatalf("healthz: %d %+v", c, h)
	}
	if c := anon.do("GET", "/metrics", nil, nil); c != 401 {
		t.Fatalf("metrics without a token: %d", c)
	}
	var tok struct{ Token string }
	if c := e.do("POST", "/api/v1/tokens", map[string]any{"name": "prom", "scopes": []string{"read"}}, &tok); c != 200 {
		t.Fatalf("token: %d", c)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`unagikuma_monitor_up{monitor="web",type="HTTP",group="public",area=""}`, "unagikuma_build_info", "# TYPE unagikuma_monitor_status gauge",
		"unagikuma_rounds_started_total", "unagikuma_store_flushes_total", `unagikuma_db_bytes{db="history"}`, "unagikuma_goroutines"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("metrics lack %s:\n%s", want, body)
		}
	}
}
