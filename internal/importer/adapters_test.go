package importer

import (
	"github.com/githubflyideas/unagikuma/internal/config"
	"strings"
	"testing"
	"time"
)

func TestGatusSafety(t *testing.T) {
	for _, field := range []string{"conditions: ['[BODY].ok == true']", "conditions: []", "url: https://user:FAKEpass@example.invalid/?token=FAKEtoken", "headers: {Authorization: '$TOKEN'}", "client: {tls: {private-key-file: /etc/key}}", "interval: 500ms", "graphql: true", "mystery-option: true"} {
		t.Run(field, func(t *testing.T) {
			base := "endpoints:\n  - name: safe\n    url: https://example.invalid\n    conditions: ['[STATUS] == 200']\n"
			// Replace rather than duplicate the overridden YAML field.
			key := strings.SplitN(field, ":", 2)[0]
			lines := strings.Split(base, "\n")
			var keep []string
			for _, l := range lines {
				if !strings.HasPrefix(l, "    "+key+":") {
					keep = append(keep, l)
				}
			}
			r, err := Gatus([]byte(strings.Join(keep, "\n")+"    "+field+"\n"), nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if r.Monitors != 0 || len(r.Records) != 0 || r.Items[0].Status != StatusLeftOut {
				t.Fatalf("unsafe activation: %+v", r)
			}
			if strings.Contains(r.Text+r.Report(ReportOptions{}), "FAKE") {
				t.Fatal("credential leak")
			}
		})
	}
	for _, input := range []string{"endpoints: &a [*a]", "endpoints: []\n---\nendpoints: []", "endpoints: []\nendpoints: []", strings.Repeat("x", (1<<20)+1), "endpoints: " + strings.Repeat("[", 40) + strings.Repeat("]", 40)} {
		if _, err := Gatus([]byte(input), nil, Options{}); err == nil {
			t.Fatal("accepted unsafe YAML")
		}
	}
}

func TestGatusSecretsAndCollisions(t *testing.T) {
	data := []byte("endpoints:\n  - name: one\n    url: https://example.invalid\n    headers: {Authorization: 'Bearer FAKEsecret', X-Private: FAKEprivate}\n    body: FAKEbody\n    method: POST\n    conditions: ['[STATUS] == 200']\n  - name: two\n    url: https://example.invalid\n    conditions: ['[STATUS] == 200']\n")
	r, err := Gatus(data, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Monitors != 2 || len(r.Secrets) != 3 {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.Text+r.Report(ReportOptions{}), "FAKE") {
		t.Fatal("secret leak")
	}
	_, cfg, ds := config.Load("test.bind", []byte("@ IN SOA ns hostmaster ( 1 1m 15s 5s 3 )\n"+r.Text), config.Options{SecretKnown: func(string) bool { return true }})
	if config.HasErrors(ds) {
		t.Fatal(ds)
	}
	again, err := Gatus(data, cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Monitors != 0 || again.Items[0].Status != StatusLeftOut {
		t.Fatalf("collision silently renamed: %+v", again)
	}
}

func TestCheckmateExport(t *testing.T) {
	data := []byte(`[{"name":"CM health","type":"http","url":"https://example.invalid","method":"HEAD","interval":90000,"isActive":false,"proxyMode":"none","statusWindowSize":1,"statusWindowThreshold":100,"customUpCodes":[404],"secret":"FAKEcm","teamId":"untrusted","userId":"admin"}]`)
	r, err := Checkmate(data, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Monitors != 1 || len(r.Users) != 0 || len(r.Secrets) != 1 || strings.Contains(r.Text+r.Report(ReportOptions{}), "FAKEcm") || !strings.Contains(r.Text, "200-299,404") || !strings.Contains(r.Text, "disabled=yes") {
		t.Fatalf("bad export mapping: %+v", r)
	}
	_, _, ds := config.Load("test.bind", []byte("@ IN SOA ns hostmaster ( 1 1m 15s 5s 3 )\n"+r.Text), config.Options{SecretKnown: func(string) bool { return true }})
	if config.HasErrors(ds) {
		t.Fatal(ds)
	}
	for _, replace := range [][2]string{{`"http"`, `"hardware"`}, {`"proxyMode":"none"`, `"proxyMode":"inherit"`}, {`"statusWindowSize":1`, `"statusWindowSize":5`}, {`"customUpCodes":[404]`, `"customUpCodes":[404],"useAdvancedMatching":true`}, {`"secret":"FAKEcm"`, `"secret":"${HOST_SECRET}"`}, {`"interval":90000`, `"interval":90001`}} {
		r, err := Checkmate([]byte(strings.Replace(string(data), replace[0], replace[1], 1)), nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Monitors != 0 || r.ActivationError() == nil {
			t.Fatalf("unsafe: %+v", r)
		}
	}
	for _, s := range []string{`{"monitors":[]}`, `[{"name":"x","name":"y"}]`, strings.Repeat("[", 40) + strings.Repeat("]", 40), `[] []`, `null`} {
		if _, err := Checkmate([]byte(s), nil, Options{}); err == nil {
			t.Fatal("accepted invalid JSON shape")
		}
	}
}

func TestAdapterAdditionalSafety(t *testing.T) {
	r, err := Checkmate([]byte(`[{"name":"unsupported","type":"hardware","url":"https://example.invalid","interval":60000,"isActive":true,"proxyMode":"none","statusWindowSize":1,"statusWindowThreshold":100}]`), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Type != "hardware" {
		t.Fatalf("misleading type: %+v", r.Items)
	}
	// Source metadata never changes destination scope, accounts or push tokens.
	r, err = Checkmate([]byte(`[{"name":"safe","type":"http","url":"https://example.invalid","interval":60000,"isActive":false,"proxyMode":"none","statusWindowSize":1,"statusWindowThreshold":100,"tenantId":"source-tenant","userId":"root","teamId":"source-team","port":443}]`), nil, Options{Area: "region:destination"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Blocked || r.Area != "region:destination" || len(r.Users) != 0 || len(r.PushTokens) != 0 {
		t.Fatalf("foreign authority or HTTP metadata failure: %+v", r)
	}
}

func TestGatusHTTPGeneratedConfig(t *testing.T) {
	r, err := Gatus([]byte(`endpoints:
  - name: API health
    group: Services
    url: https://example.invalid/health
    interval: 90s
    method: HEAD
    conditions: ["[STATUS] == 204"]
`), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Monitors != 1 {
		t.Fatalf("%+v", r)
	}
	_, cfg, ds := config.Load("import.bind", []byte("@ IN SOA ns hostmaster ( 1 1m 15s 5s 3 )\n"+r.Text), config.Options{})
	if config.HasErrors(ds) {
		t.Fatalf("generated configuration: %v\n%s", ds, r.Text)
	}
	m := cfg.FindMonitor("api-health")
	if m == nil || m.Interval != 90*time.Second || m.Group != "Services" || m.Title != "API health" {
		t.Fatalf("monitor: %+v", m)
	}
	if !strings.Contains(r.Text, "expect=204") || !strings.Contains(r.Text, "method=HEAD") {
		t.Fatal(r.Text)
	}
}
