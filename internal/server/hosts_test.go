package server

import "testing"

func TestHosts(t *testing.T) {
	e := newEnv(t, `@ IN SOA unagikuma. admin.localhost. ( 2026102301 1s 500ms 300ms 2 )
$SET engine.first-round-delay=0s suspend.recheck-spread=0s
gw      IN PING 192.168.1.1
gw-ssh  IN TCP  192.168.1.1:22
www     IN HTTP http://192.168.1.10/ device=web1
www-ssh IN TCP  192.168.1.10:22 device=web1
backup  IN PUSH period=1h
`, nil)
	e.setup()
	var res struct {
		Items []hostJSON `json:"items"`
	}
	byName := map[string]hostJSON{}
	waitUntil(t, func() bool {
		e.do("GET", "/api/v1/hosts", nil, &res)
		for _, h := range res.Items {
			byName[h.Name] = h
		}
		return len(res.Items) == 3 && byName["192.168.1.1"].Status == "up" && byName["web1"].Status == "up"
	})
	gw, web := byName["192.168.1.1"], byName["web1"]
	if gw.Check != "gw" || gw.Total != 2 || web.Total != 2 || web.Address != "192.168.1.10" || web.Check != "" {
		t.Fatalf("hosts: %+v", res.Items)
	}
	if b := byName["backup"]; b.Total != 1 || b.Status != "pending" {
		t.Fatalf("push host: %+v", b)
	}
	var rows struct {
		Items []rowJSON `json:"items"`
	}
	if e.do("GET", "/api/v1/monitors?q=host:web1", nil, &rows); len(rows.Items) != 2 {
		t.Fatalf("host filter: %d", len(rows.Items))
	}
}

func TestTacticalAndAvailability(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	var ov struct {
		Tactical struct {
			Services map[string]struct{ Total, Unhandled, Handled int } `json:"services"`
			Hosts    map[string]int                                     `json:"hosts"`
		} `json:"tactical"`
	}
	waitUntil(t, func() bool {
		e.do("GET", "/api/v1/overview", nil, &ov)
		return ov.Tactical.Services["ok"].Total == 2
	})
	if ov.Tactical.Hosts["up"] != 2 {
		t.Fatalf("hosts: %+v", ov.Tactical.Hosts)
	}
	var av struct {
		Range string `json:"range"`
		Items []struct {
			Name  string   `json:"name"`
			Ratio *float64 `json:"ratio"`
		} `json:"items"`
	}
	if e.do("GET", "/api/v1/reports/availability?range=7d", nil, &av) != 200 || av.Range != "7d" || len(av.Items) != 3 {
		t.Fatalf("availability: %+v", av)
	}
}

func TestDailyReportAPI(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	var rep struct {
		Title, Body, Policy, At string
		OK, Enabled             bool
	}
	waitUntil(t, func() bool {
		e.do("GET", "/api/v1/reports/daily", nil, &rep)
		return rep.OK
	})
	if !rep.Enabled || rep.At != "10:00" || rep.Policy == "" || rep.Body == "" {
		t.Fatalf("report: %+v", rep)
	}
	if code := e.do("POST", "/api/v1/reports/daily/send", map[string]any{}, &rep); code != 200 {
		t.Fatalf("send: %d", code)
	}
}
