package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBoundedImportsAPI(t *testing.T) {
	cases := map[string]string{
		"gatus":     "endpoints:\n  - name: imported-http\n    url: https://example.invalid\n    enabled: false\n    headers: {Authorization: 'Bearer FAKE-api-secret'}\n    conditions: ['[STATUS] == 200']\n",
		"checkmate": `[{"name":"imported-http","type":"http","url":"https://example.invalid","method":"GET","interval":60000,"isActive":false,"proxyMode":"none","statusWindowSize":1,"statusWindowThreshold":100,"secret":"FAKE-api-secret","teamId":"other-team","userId":"root"}]`,
	}
	for source, data := range cases {
		t.Run(source, func(t *testing.T) {
			e := newEnv(t, testBind, nil)
			e.setup()
			post := func(body, query string) (int, map[string]any) {
				req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/import/"+source+query, strings.NewReader(body))
				req.Header.Set("X-Unagikuma-CSRF", e.csrf)
				resp, err := e.c.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if strings.Contains(string(raw), "FAKE-api-secret") {
					t.Fatal("API leaked credential")
				}
				var out map[string]any
				json.Unmarshal(raw, &out)
				return resp.StatusCode, out
			}
			serial := e.app.Config().Serial
			code, prev := post(data, "")
			if code != 200 || prev["monitors"] != float64(1) {
				t.Fatalf("preview %d %+v", code, prev)
			}
			if code, _ := post(strings.Repeat("x", (1<<20)+1), ""); code != http.StatusRequestEntityTooLarge {
				t.Fatalf("upload bound: %d", code)
			}
			if e.app.Config().Serial != serial {
				t.Fatal("preview wrote config")
			}
			code, out := post(data, "?apply=1")
			if code != 200 {
				t.Fatalf("apply %d %+v", code, out)
			}
			if m := e.app.Config().Config.FindMonitor("imported-http"); m == nil || !m.Disabled {
				t.Fatalf("missing/active monitor: %+v", m)
			}
			secretsBefore, err := e.app.SecretStore().List()
			if err != nil || len(secretsBefore) != 1 {
				t.Fatalf("credentials not persisted: %v %v", secretsBefore, err)
			}
			secretName := "checkmate_imported_http_bearer"
			expectedSecret := "FAKE-api-secret"
			if source == "gatus" {
				secretName = "gatus_imported_http_authorization"
				expectedSecret = "Bearer FAKE-api-secret"
			}
			if value, err := e.app.SecretStore().Get(secretName); err != nil || value != expectedSecret {
				t.Fatal("credential value not preserved")
			}
			serial = e.app.Config().Serial
			code, _ = post(data, "?apply=1")
			if code != 400 || e.app.Config().Serial != serial {
				t.Fatal("collision was not rejected without mutation")
			}
			bad := strings.ReplaceAll(data, "imported-http", "unsafe-http")
			if source == "gatus" {
				bad = strings.ReplaceAll(bad, "[STATUS] == 200", "[BODY].ok == true")
			} else {
				bad = strings.ReplaceAll(bad, `"type":"http"`, `"type":"hardware"`)
			}
			code, p := post(bad, "")
			if code != 200 || p["blocked"] != true {
				t.Fatalf("unsafe preview %d %+v", code, p)
			}
			code, _ = post(bad, "?apply=1")
			if code != 400 || e.app.Config().Serial != serial {
				t.Fatal("unsafe apply modified config")
			}
			secretsAfter, err := e.app.SecretStore().List()
			if err != nil || len(secretsAfter) != len(secretsBefore) {
				t.Fatal("rejected import changed secret inventory")
			}
			if value, err := e.app.SecretStore().Get(secretName); err != nil || value != expectedSecret {
				t.Fatal("rejected import changed existing credential")
			}
		})
	}
}
