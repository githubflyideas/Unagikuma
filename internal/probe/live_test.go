//go:build live

package probe

import (
	"strings"
	"testing"
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
