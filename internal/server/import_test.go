package server

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestKumaImportAPI(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	data, err := os.ReadFile("../importer/testdata/kuma.json")
	if err != nil {
		t.Fatal(err)
	}
	post := func(q string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/import/kuma"+q, strings.NewReader(string(data)))
		req.Header.Set("X-Unagikuma-CSRF", e.csrf)
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	before := e.app.Config().Serial
	code, prev := post("")
	if code != 200 || prev["monitors"].(float64) != 10 || !strings.Contains(prev["text"].(string), "IN CHANNEL feishu") {
		t.Fatalf("preview: %d %v", code, prev)
	}
	if e.app.Config().Serial != before || e.app.SecretStore().Has("kuma_channel_url") {
		t.Fatal("a preview must not change anything")
	}
	code, done := post("?apply=1")
	if code != 200 || done["serial"] == nil {
		t.Fatalf("apply: %d %v", code, done)
	}
	cfg := e.app.Config().Config
	if cfg.FindMonitor("api-health") == nil || cfg.FindChannel("tg-ops") == nil || !e.app.SecretStore().Has("kuma_tg_ops_token") {
		t.Fatal("import not applied")
	}
	// Importing the same backup again changes nothing.
	serial := e.app.Config().Serial
	if code, again := post("?apply=1"); code != 200 || again["monitors"].(float64) != 0 || e.app.Config().Serial != serial {
		t.Fatalf("second import: %d %v", code, again)
	}
}

func TestKumaDatabaseImportAPI(t *testing.T) {
	e := newEnv(t, usersBind, nil)
	e.setup()
	if c := e.do("POST", "/api/v1/users", map[string]any{"username": "lead", "password": "secret123", "role": "viewer"}, nil); c != 200 {
		t.Fatalf("create viewer: %d", c)
	}
	path := filepath.Join(t.TempDir(), "kuma.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("kuma-pass-1"), 4)
	for _, q := range []string{
		`CREATE TABLE user (id INTEGER PRIMARY KEY, username TEXT, password TEXT, active BOOLEAN)`,
		`CREATE TABLE monitor (id INTEGER PRIMARY KEY, name TEXT, active BOOLEAN, interval INT, hostname TEXT, port INT, type TEXT)`,
		`INSERT INTO user VALUES (1, 'kumaadmin', '` + string(hash) + `', 1)`,
		`INSERT INTO monitor VALUES (1, 'BJ core', 1, 30, '10.10.0.9', 22, 'port')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	data, _ := os.ReadFile(path)
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/import/kuma?apply=1&area=region:north&role=operator&manager=lead", strings.NewReader(string(data)))
	req.Header.Set("X-Unagikuma-CSRF", e.csrf)
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"created"`) || !strings.Contains(string(b), `"manager"`) {
		t.Fatalf("import: %d %s", resp.StatusCode, b)
	}
	if m := e.app.Config().Config.FindMonitor("bj-core"); m == nil || m.Area != "region:north" {
		t.Fatalf("monitor: %+v", m)
	}
	// Uptime Kuma's account logs in with its old password, limited to the section.
	k := e.loginAs("kumaadmin", "kuma-pass-1")
	var sess struct {
		Role  string   `json:"role"`
		Areas []string `json:"areas"`
	}
	k.do("GET", "/api/v1/session", nil, &sess)
	if sess.Role != "operator" || len(sess.Areas) != 1 || sess.Areas[0] != "region:north" {
		t.Fatalf("kuma account: %+v", sess)
	}
	l := e.loginAs("lead", "secret123")
	l.do("GET", "/api/v1/session", nil, &sess)
	if sess.Role != "operator" || len(sess.Areas) != 1 {
		t.Fatalf("manager: %+v", sess)
	}
}

func TestNagiosImportAPI(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	data, err := os.ReadFile("../importer/testdata/nagios.cfg")
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", e.ts.URL+path, strings.NewReader(string(data)))
		req.Header.Set("X-Unagikuma-CSRF", e.csrf)
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := post("/api/v1/import/zabbix"); code != 404 {
		t.Fatalf("unknown source: %d", code)
	}
	code, done := post("/api/v1/import/nagios?apply=1")
	if code != 200 || done["monitors"] == nil {
		t.Fatalf("apply: %d %v", code, done)
	}
	cfg := e.app.Config().Config
	if cfg.FindMonitor("web1-site") == nil || cfg.FindChannel("admins") == nil || cfg.FindPeriod("workhours") == nil {
		t.Fatal("import not applied")
	}
}
