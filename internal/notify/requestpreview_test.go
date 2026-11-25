package notify

import (
	"errors"
	"strings"
	"testing"
)

// Every kind that sends an HTTP request shows it for an example problem,
// from an empty form as well, and never shows a credential.
func TestPreviewRequestEveryKind(t *testing.T) {
	const secret = "S3cretValue123456"
	for _, k := range Kinds() {
		pv, err := PreviewRequest(k.Kind, map[string]string{}, "zh", "host1")
		if errors.Is(err, ErrNoHTTP) {
			if k.Kind != "email" && k.Kind != "log" && k.Kind != "group" {
				t.Errorf("%s: no request", k.Kind)
			}
			continue
		}
		if err != nil || pv.Method == "" || pv.URL == "" {
			t.Errorf("%s: %v %+v", k.Kind, err, pv)
			continue
		}
		// With a real credential typed in, it is masked everywhere.
		o := map[string]string{}
		for _, f := range k.Fields {
			if f.Type == "secret" {
				o[f.Key] = secret
				if f.Key == "url" {
					o[f.Key] = "https://hooks.example.com/" + secret
				}
			}
		}
		if len(o) == 0 {
			continue
		}
		pv, err = PreviewRequest(k.Kind, o, "en", "host1")
		if err != nil {
			t.Errorf("%s with secrets: %v", k.Kind, err)
			continue
		}
		all := pv.URL + pv.Body
		for _, h := range pv.Headers {
			all += h[1]
		}
		if strings.Contains(all, secret) {
			t.Errorf("%s shows the secret: %s %v %s", k.Kind, pv.URL, pv.Headers, pv.Body)
		}
	}
}
