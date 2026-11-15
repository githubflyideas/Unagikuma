//go:build live

package probe

import (
	"strings"
	"testing"
	"time"

	"github.com/githubflyideas/unagikuma/internal/appprofile"
	"github.com/githubflyideas/unagikuma/internal/valuerule"
)

// Checks every application profile against the real application, started
// by scripts/live/start.sh. Not part of the normal tests: they need the
// applications installed. A profile's "verified" says which version it
// was last run against.
func TestLiveApps(t *testing.T) {
	for _, c := range []struct {
		app, url string
		field    string
		min, max float64 // the judged number must lie in [min, max]
	}{
		{"nginx", "http://127.0.0.1:18080/nginx_status", "active", 1, 100},
		{"nginx", "http://127.0.0.1:18080/nginx_status", "requests", 1, 1e9},
		{"apache", "http://127.0.0.1:18081/server-status?auto", "busy", 1, 100},
		{"lighttpd", "http://127.0.0.1:18082/server-status?auto", "busy", 0, 100},
		{"php-fpm", "http://127.0.0.1:18080/status", "total", 1, 10},
		{"haproxy", "http://127.0.0.1:18084/metrics", "sessions", 0, 100},
		{"haproxy", "http://127.0.0.1:18084/metrics", "servers_down", 1, 1}, // s2 is a closed port
		{"haproxy", "http://127.0.0.1:18084/metrics", "backends_up", 1, 1},
	} {
		r := run(t, Spec{Type: "HTTP", Target: c.url, Opts: map[string]string{"app": c.app, "field": c.field}})
		if r.Vote != VoteOK {
			t.Errorf("%s %s: %s %s", c.app, c.field, r.Reason, r.Detail)
			continue
		}
		if v := r.Metrics["value"]; v < c.min || v > c.max {
			t.Errorf("%s %s = %v, want %v..%v (%s)", c.app, c.field, v, c.min, c.max, r.Detail)
		}
		if !strings.Contains(r.Detail, ":") {
			t.Errorf("%s: detail %q", c.app, r.Detail)
		}
	}
}

// A real Redis (redis-server --port 16379): INFO is read and each field
// of the profile is found.
func TestLiveRedis(t *testing.T) {
	for _, f := range redisProfile().Fields {
		if f.Optional {
			continue
		}
		r := run(t, Spec{Type: "TCP", Target: "127.0.0.1:16379", Opts: map[string]string{"proto": "redis", "field": f.Key}})
		if r.Vote != VoteOK {
			t.Errorf("redis %s: %s %s", f.Key, r.Reason, r.Detail)
		}
	}
	r := run(t, Spec{Type: "TCP", Target: "127.0.0.1:16379", Opts: map[string]string{"proto": "redis", "field": "connected_clients", "role": "master"}})
	if r.Vote != VoteOK || r.Metrics["value"] < 1 {
		t.Errorf("redis role: %+v", r)
	}
}

// Real PostgreSQL (port 15432) and MariaDB (port 13306) with a read-only
// account "monitor": a query returns the number, and the session cannot
// write.
func TestLiveSQL(t *testing.T) {
	pg := func(user, pass, q string) Result {
		return run(t, Spec{Type: "TCP", Target: "127.0.0.1:15432", Opts: map[string]string{"proto": "postgres", "user": user, "password": pass, "database": "postgres", "query": q}})
	}
	my := func(q string) Result {
		return run(t, Spec{Type: "TCP", Target: "127.0.0.1:13306", Opts: map[string]string{"proto": "mysql", "user": "monitor", "password": "monpass1234", "database": "app", "query": q}})
	}
	if r := pg("monitor", "monpass1234", "SELECT count(*) FROM orders"); r.Vote != VoteOK || r.Metrics["value"] != 42 {
		t.Errorf("pg count: %+v", r)
	}
	if r := pg("monitor", "monpass1234", "SELECT sum(amount) FROM orders;"); r.Vote != VoteOK || r.Metrics["value"] != 903 {
		t.Errorf("pg sum: %+v", r)
	}
	for q, why := range map[string]string{"SELECT 'abc'": "not a number", "SELECT NULL::int": "NULL", "SELECT 1 WHERE false": "no rows", "SELECT * FROM nope": "failed"} {
		if r := pg("monitor", "monpass1234", q); r.Vote != VoteFail || !strings.Contains(r.Detail, why) {
			t.Errorf("pg %q: %+v", q, r)
		}
	}
	// The session is read-only whoever logs in: even a superuser cannot
	// write through a SELECT.
	if r := pg("postgres", "supersecret", "SELECT nextval('orders_id_seq')"); r.Vote != VoteFail || !strings.Contains(r.Detail, "read-only") {
		t.Errorf("pg write through SELECT: %+v", r)
	}
	if r := my("SELECT count(*) FROM orders"); r.Vote != VoteOK || r.Metrics["value"] != 3 {
		t.Errorf("mysql count: %+v", r)
	}
	if r := my("SELECT SUM(amount) FROM orders"); r.Vote != VoteOK || r.Metrics["value"] != 6.5 {
		t.Errorf("mysql sum: %+v", r)
	}
	if r := my("SELECT @@session.tx_read_only"); r.Vote != VoteOK || r.Metrics["value"] != 1 {
		t.Errorf("mysql session is not read-only: %+v", r)
	}
	for q, why := range map[string]string{"SELECT 'abc'": "not a number", "SELECT NULL": "NULL", "SELECT 1 FROM orders WHERE 1=0": "no rows", "SELECT * FROM nope": "failed"} {
		if r := my(q); r.Vote != VoteFail || !strings.Contains(r.Detail, why) {
			t.Errorf("mysql %q: %+v", q, r)
		}
	}
}

// Every required field of the metrics-page profiles is found on the real
// applications (scripts/live/metrics.sh starts them).
func TestLiveMetricsApps(t *testing.T) {
	for app, url := range map[string]string{
		"prometheus": "http://127.0.0.1:19090/metrics",
		"etcd":       "http://127.0.0.1:12379/metrics",
		"caddy":      "http://127.0.0.1:12019/metrics",
		"rabbitmq":   "http://127.0.0.1:15692/metrics",
		"grafana":    "http://127.0.0.1:13000/metrics",
		"traefik":    "http://127.0.0.1:18087/metrics",
		"minio":      "http://127.0.0.1:19100/minio/v2/metrics/cluster",
	} {
		p, ok := appprofile.Get(app)
		if !ok {
			t.Fatalf("no profile %s", app)
		}
		for _, f := range p.Fields {
			if f.Optional {
				continue
			}
			r := run(t, Spec{Type: "HTTP", Target: url, Opts: map[string]string{"app": app, "field": f.Key}})
			if r.Vote != VoteOK {
				t.Errorf("%s %s: %s %s", app, f.Key, r.Reason, r.Detail)
			}
		}
	}
	// Traefik reports its backend servers only once a service exists; the
	// live setup has one healthy server.
	for _, f := range []string{"servers_all_up", "servers", "requests"} {
		if r := run(t, Spec{Type: "HTTP", Target: "http://127.0.0.1:18087/metrics", Opts: map[string]string{"app": "traefik", "field": f}}); r.Vote != VoteOK {
			t.Errorf("traefik %s: %s %s", f, r.Reason, r.Detail)
		}
	}
	// etcd's leader is a 1 on a healthy node.
	if r := run(t, Spec{Type: "HTTP", Target: "http://127.0.0.1:12379/metrics", Opts: map[string]string{"app": "etcd", "field": "has_leader", "crit": "<1"}}); r.Vote != VoteOK || r.Metrics["value"] != 1 {
		t.Errorf("etcd leader: %+v", r)
	}
}

// A condition of two fields, judged on real Apache and Redis: the numbers
// are the ones the servers report now.
func TestLiveConditions(t *testing.T) {
	for _, c := range []struct {
		name string
		spec Spec
		want Vote
	}{
		{"apache and, both hold", Spec{Type: "HTTP", Target: "http://127.0.0.1:18081/server-status?auto", Opts: map[string]string{"app": "apache", "crit": "busy>=1 and idle>10"}}, VoteFail},
		{"apache and, one does not", Spec{Type: "HTTP", Target: "http://127.0.0.1:18081/server-status?auto", Opts: map[string]string{"app": "apache", "crit": "busy>=1 and idle>1000"}}, VoteOK},
		{"apache or, one holds", Spec{Type: "HTTP", Target: "http://127.0.0.1:18081/server-status?auto", Opts: map[string]string{"app": "apache", "crit": "busy>50 or idle>10"}}, VoteFail},
		{"redis and, both hold", Spec{Type: "TCP", Target: "127.0.0.1:16379", Opts: map[string]string{"proto": "redis", "field": "connected_clients", "crit": "connected_clients>=1 and used_memory>1KB"}}, VoteFail},
		{"redis and, memory limit not reached", Spec{Type: "TCP", Target: "127.0.0.1:16379", Opts: map[string]string{"proto": "redis", "field": "connected_clients", "crit": "connected_clients>=1 and used_memory>1GB"}}, VoteOK},
	} {
		// probe.Run reads the numbers; the engine judges them with the rules.
		r := run(t, c.spec)
		if r.Vote != VoteOK {
			t.Errorf("%s: read failed: %s %s", c.name, r.Reason, r.Detail)
			continue
		}
		rules, err := valuerule.ParseWith(func(k string) string { return c.spec.Opts[k] }, ValueUnit(c.spec), ValueFields(c.spec))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		out := (&valuerule.Tracker{Rules: rules}).Observe(time.Now(), r.Metrics)
		if got := out.Level == valuerule.Crit; got != (c.want == VoteFail) {
			t.Errorf("%s: level %v, %s", c.name, out.Level, out.Detail)
		}
	}
}
