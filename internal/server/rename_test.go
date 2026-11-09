package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Renaming a monitor keeps its id, its history and its open incident,
// whether renamed through the API or with was= in the file.
func TestRenameKeepsHistory(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	row := func(name string) map[string]any {
		var r struct{ Row map[string]any }
		e.do("GET", "/api/v1/monitors/"+name, nil, &r)
		return r.Row
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.app.Store().Flush()
		// A few rounds, so a fresh id (one round at most) would show.
		if r := row("db"); r["coverage_24h"].(float64)*86400 >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no history for db: %v", row("db"))
		}
		time.Sleep(200 * time.Millisecond)
	}
	id, before := row("db")["id"], row("db")["coverage_24h"].(float64)
	if code := e.do("PUT", "/api/v1/monitors/db", map[string]any{"record": map[string]any{"name": "pg", "type": "TCP", "target": "192.168.1.20:5432"}}, nil); code != 200 {
		t.Fatalf("rename: %d", code)
	}
	r := row("pg")
	// A fresh id would start from nothing; the carried history covers at
	// least what db had.
	if r["id"] != id || r["coverage_24h"].(float64) < before*0.99 {
		t.Fatalf("after API rename: id %v (was %v), coverage %v (was %v)", r["id"], id, r["coverage_24h"], before)
	}

	// By hand: the new name with was= and a higher serial.
	path := filepath.Join(e.app.DataDir(), "monitors.conf")
	b, _ := os.ReadFile(path)
	text := regexp.MustCompile(`(?m)^pg(\s+IN TCP 192\.168\.1\.20:5432)`).ReplaceAllString(string(b), "postgres$1 was=pg")
	m := regexp.MustCompile(`\( (\d+) `).FindStringSubmatch(text)
	n, _ := strconv.ParseInt(m[1], 10, 64)
	text = strings.Replace(text, m[0], "( "+itoa(n+1)+" ", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for e.app.Config().Config.FindMonitor("postgres") == nil {
		if time.Now().After(deadline) {
			t.Fatalf("file edit not applied:\n%s", text)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if r := row("postgres"); r["id"] != id || !hasHistory(r) {
		t.Fatalf("after was= rename: %v", r)
	}
}

// hasHistory: some of the day was watched (the uptime figure itself waits
// for uptime.min-coverage).
func hasHistory(row map[string]any) bool {
	c, _ := row["coverage_24h"].(float64)
	return c > 0
}
