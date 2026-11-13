package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
)

// The external heartbeat is a plain GET unless the user shapes it: services
// differ (Healthchecks takes a GET or a POST with a log line, an Uptime Kuma
// push monitor reads status and msg from the query, others want JSON), so
// the method, headers and body can be set, and the URL and body can carry
// a few facts about this machine.

// heartbeatMethods are the methods a heartbeat may use.
var heartbeatMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true}

var heartbeatFuncs = template.FuncMap{
	// json writes a value as a JSON literal, for use inside a JSON body.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
}

// parseHeartbeatHeaders reads one "Name: value" per line.
func parseHeartbeatHeaders(s string) (http.Header, error) {
	h := http.Header{}
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("heartbeat header line %d: write it as Name: value", i+1)
		}
		h.Add(textproto.CanonicalMIMEHeaderKey(name), strings.TrimSpace(value))
	}
	return h, nil
}

// checkHeartbeatSetting validates one of the heartbeat settings.
func checkHeartbeatSetting(key, value string) error {
	switch key {
	case "heartbeat_method":
		if value != "" && !heartbeatMethods[strings.ToUpper(value)] {
			return fmt.Errorf("heartbeat method must be GET, HEAD, POST or PUT")
		}
	case "heartbeat_headers":
		_, err := parseHeartbeatHeaders(value)
		return err
	case "heartbeat_url", "heartbeat_body":
		if _, err := template.New(key).Funcs(heartbeatFuncs).Option("missingkey=error").Parse(value); err != nil {
			return fmt.Errorf("%s: %v", strings.TrimPrefix(key, "heartbeat_"), err)
		}
	}
	return nil
}

// heartbeatData is what a heartbeat URL or body can insert.
// The monitor counts come from the engine loop, so they are read only when
// the URL or body asks for them.
func (a *App) heartbeatData(counts bool) map[string]any {
	now := a.now()
	d := map[string]any{"Instance": a.instance(), "Status": "ok", "Problems": 0, "Monitors": 0,
		"Time": now.UTC().Format(time.RFC3339), "Unix": now.Unix(),
		"Uptime": int64(now.Sub(a.started).Seconds()), "Version": a.opts.Version}
	if counts && a.engine != nil {
		ov := a.engine.Overview()
		n := 0
		for _, c := range ov.Counts {
			n += c
		}
		d["Monitors"], d["Problems"] = n, ov.Unhandled
		if ov.Unhandled > 0 {
			d["Status"] = "problems"
		}
	}
	return d
}

// prepareHeartbeat builds the call from the URL and the heartbeat settings.
// Values inserted into the URL are escaped for it.
func (a *App) prepareHeartbeat(raw string) (engine.HeartbeatRequest, error) {
	body := a.Setting("heartbeat_body")
	needs := false
	for _, k := range []string{".Status", ".Problems", ".Monitors"} {
		needs = needs || strings.Contains(raw, k) || strings.Contains(body, k)
	}
	data := a.heartbeatData(needs)
	render := func(name, src string, esc func(string) string) (string, error) {
		if !strings.Contains(src, "{{") {
			return src, nil
		}
		vals := data
		if esc != nil {
			vals = map[string]any{}
			for k, v := range data {
				vals[k] = esc(fmt.Sprint(v))
			}
		}
		t, err := template.New(name).Funcs(heartbeatFuncs).Option("missingkey=error").Parse(src)
		if err != nil {
			return "", fmt.Errorf("heartbeat %s: %v", name, err)
		}
		var b bytes.Buffer
		if err := t.Execute(&b, vals); err != nil {
			return "", fmt.Errorf("heartbeat %s: %v", name, err)
		}
		return b.String(), nil
	}
	hr := engine.HeartbeatRequest{Method: strings.ToUpper(a.Setting("heartbeat_method"))}
	if hr.Method == "" {
		hr.Method = http.MethodGet
	}
	var err error
	if hr.URL, err = render("url", raw, url.QueryEscape); err != nil {
		return hr, err
	}
	if hr.Header, err = parseHeartbeatHeaders(a.Setting("heartbeat_headers")); err != nil {
		return hr, err
	}
	if hr.Method == http.MethodPost || hr.Method == http.MethodPut {
		if hr.Body, err = render("body", body, nil); err != nil {
			return hr, err
		}
		if hr.Body != "" && hr.Header.Get("Content-Type") == "" {
			ct := "text/plain; charset=utf-8"
			if json.Valid([]byte(hr.Body)) {
				ct = "application/json"
			}
			hr.Header.Set("Content-Type", ct)
		}
	}
	return hr, nil
}
