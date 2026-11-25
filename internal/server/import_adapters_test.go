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
			// The same import again: everything is already here, nothing changes.
			code, _ = post(data, "?apply=1")
			if code >= 500 || e.app.Config().Serial != serial {
				t.Fatalf("a second run changed the configuration: %d", code)
			}
			// A monitor that cannot be imported (a Gatus URL that is
			// still a placeholder, a Checkmate hardware monitor) is
			// reported and never created; it no longer blocks the
			// import of everything else, and applying its preview
			// changes nothing.
			bad := strings.ReplaceAll(data, "imported-http", "unsafe-http")
			if source == "gatus" {
				bad = strings.ReplaceAll(bad, "https://example.invalid", "https://${IMPORT_HOST}")
			} else {
				bad = strings.ReplaceAll(bad, `"type":"http"`, `"type":"hardware"`)
			}
			code, p := post(bad, "")
			if code != 200 || p["monitors"] != float64(0) {
				t.Fatalf("unimportable preview %d %+v", code, p)
			}
			if items, ok := p["items"].([]any); !ok || len(items) != 1 || items[0].(map[string]any)["status"] != "left-out" {
				t.Fatalf("unimportable item not reported as left out: %+v", p)
			}
			code, _ = post(bad, "?apply=1")
			if code >= 400 || e.app.Config().Serial != serial {
				t.Fatal("applying an unimportable preview modified config")
			}
			if m := e.app.Config().Config.FindMonitor("unsafe-http"); m != nil {
				t.Fatal("an unimportable monitor was created")
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
