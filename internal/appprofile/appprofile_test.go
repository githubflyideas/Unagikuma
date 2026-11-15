package appprofile

import (
	"strings"
	"testing"
)

// Sample replies, written from each application's documented format.
const (
	nginxPage = "Active connections: 291 \nserver accepts handled requests\n 16630948 16630940 31070465 \nReading: 6 Writing: 179 Waiting: 106 \n"

	apachePage = "ServerVersion: Apache/2.4.58 (Unix)\nTotal Accesses: 3039\nTotal kBytes: 1337\nCPULoad: .0168\nUptime: 33172\nReqPerSec: .0916\nBytesPerSec: 41.2\nBusyWorkers: 3\nIdleWorkers: 7\nScoreboard: _W_W_\n"

	lighttpdPage = "Total Accesses: 1234\nTotal kBytes: 2345\nUptime: 4321\nBusyServers: 2\nIdleServers: 6\nScoreboard: ___\n"

	phpfpmPage = "pool:                 www\nprocess manager:      dynamic\naccepted conn:        5821\nlisten queue:         2\nmax listen queue:     4\nlisten queue len:     128\nidle processes:       3\nactive processes:     1\ntotal processes:      4\nmax active processes: 5\nmax children reached: 0\nslow requests:        1\n"

	haproxyPage = `# TYPE haproxy_frontend_current_sessions gauge
haproxy_frontend_current_sessions{proxy="www"} 12
haproxy_frontend_current_sessions{proxy="api"} 30
# TYPE haproxy_backend_current_queue gauge
haproxy_backend_current_queue{proxy="app"} 2
haproxy_backend_current_queue{proxy="static"} 0
# TYPE haproxy_backend_status gauge
haproxy_backend_status{proxy="app",state="DOWN"} 0
haproxy_backend_status{proxy="app",state="UP"} 1
haproxy_backend_status{proxy="static",state="DOWN"} 1
haproxy_backend_status{proxy="static",state="UP"} 0
haproxy_server_status{proxy="app",server="s1",state="DOWN"} 0
haproxy_server_status{proxy="app",server="s1",state="UP"} 1
haproxy_server_status{proxy="app",server="s2",state="DOWN"} 1
haproxy_server_status{proxy="app",server="s2",state="UP"} 0
haproxy_backend_active_servers{proxy="app"} 3
haproxy_backend_active_servers{proxy="static"} 1
haproxy_frontend_http_requests_total{proxy="www"} 1000
haproxy_frontend_http_requests_total{proxy="api"} 500.5
haproxy_frontend_http_responses_total{proxy="www",code="2xx"} 900
haproxy_frontend_http_responses_total{proxy="www",code="5xx"} 7
haproxy_frontend_http_responses_total{proxy="api",code="5xx"} 3
haproxy_backend_connection_errors_total{proxy="app"} 4
`
)

func TestProfilesLoad(t *testing.T) {
	if len(All()) < 5 {
		t.Fatalf("profiles: %v", IDs())
	}
	for _, p := range All() {
		if p.Verified == "" || p.Docs == "" || p.Needs == "" {
			t.Errorf("%s: says nothing of its source or what it needs", p.ID)
		}
	}
}

func TestReadPages(t *testing.T) {
	for _, c := range []struct {
		id   string
		page string
		want map[string]float64
	}{
		{"nginx", nginxPage, map[string]float64{"active": 291, "accepts": 16630948, "handled": 16630940, "requests": 31070465, "reading": 6, "writing": 179, "waiting": 106}},
		{"apache", apachePage, map[string]float64{"busy": 3, "idle": 7, "accesses": 3039, "uptime": 33172, "rps": 0.0916, "cpu": 0.0168}},
		{"lighttpd", lighttpdPage, map[string]float64{"busy": 2, "idle": 6, "accesses": 1234, "uptime": 4321}},
		{"php-fpm", phpfpmPage, map[string]float64{"active": 1, "idle": 3, "total": 4, "queue": 2, "max_children": 0, "slow": 1, "accepted": 5821}},
		{"haproxy", haproxyPage, map[string]float64{"sessions": 42, "queue": 2, "backends_up": 1, "backends_down": 1, "servers_down": 1, "servers_active": 4, "requests": 1500.5, "responses_5xx": 10, "connection_errors": 4}},
	} {
		p, _ := Get(c.id)
		got, err := p.Read([]byte(c.page), Select{})
		if err != nil {
			t.Errorf("%s: %v", c.id, err)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s %s = %v, want %v", c.id, k, got[k], v)
			}
		}
	}
}

// A page that is another application's, or an error page, is said not to
// be this application's: never read as zeros.
func TestWrongPage(t *testing.T) {
	for _, id := range []string{"nginx", "apache", "lighttpd", "php-fpm", "haproxy"} {
		p, _ := Get(id)
		for _, page := range []string{"", "<html><body>404 Not Found</body></html>", apachePage + "x"} {
			if id == "apache" && page != "" && strings.HasPrefix(page, "ServerVersion") {
				continue
			}
			if _, err := p.Read([]byte(page), Select{}); err == nil && !(id == "apache" && page == apachePage+"x") {
				t.Errorf("%s accepted %q", id, page)
			}
		}
	}
	p, _ := Get("nginx")
	if _, err := p.Read([]byte(apachePage), Select{}); err == nil || !strings.Contains(err.Error(), "not the Nginx status page") {
		t.Errorf("an Apache page read as Nginx: %v", err)
	}
	// An old Apache has no ReqPerSec: it is optional, the rest still reads.
	old := strings.ReplaceAll(strings.ReplaceAll(apachePage, "ReqPerSec: .0916\n", ""), "CPULoad: .0168\n", "")
	a, _ := Get("apache")
	if v, err := a.Read([]byte(old), Select{}); err != nil || v["busy"] != 3 {
		t.Errorf("without the optional fields: %v %v", v, err)
	}
}

func TestSelect(t *testing.T) {
	p, _ := Get("haproxy")
	sel, err := ParseSelect("proxy=www")
	if err != nil {
		t.Fatal(err)
	}
	v, err := p.Read([]byte(haproxyPage), sel)
	if err != nil {
		t.Fatal(err)
	}
	// Only the www frontend's series; a field with no series for it is
	// missing from the page, not zero.
	if v["sessions"] != 12 || v["requests"] != 1000 {
		t.Errorf("select proxy=www: %v", v)
	}
	if _, err := p.Read([]byte(haproxyPage), Select{"proxy", "nope"}); err == nil || !strings.Contains(err.Error(), "proxy=nope") {
		t.Errorf("a label nothing carries: %v", err)
	}
	if _, err := ParseSelect("proxy"); err == nil {
		t.Error("select without a value accepted")
	}
}

func TestMetricsParsing(t *testing.T) {
	got := parseMetrics([]byte("# comment\nm_a 1\nm_b{x=\"a,b\",y=\"q\\\"r\"} 2.5 1700000000\nbroken{\nm_c NaN\nm_d{} 4\n"))
	if len(got) != 3 || got[0].value != 1 || got[1].labels["x"] != "a,b" || got[1].labels["y"] != `q"r` || got[1].value != 2.5 || got[2].name != "m_d" {
		t.Fatalf("%+v", got)
	}
}

func TestDescribe(t *testing.T) {
	p, _ := Get("nginx")
	v, _ := p.Read([]byte(nginxPage), Select{})
	if d := p.Describe(v); d != "Nginx: Active connections 291, Reading 6, Writing 179, Waiting 106" {
		t.Errorf("describe: %q", d)
	}
}
