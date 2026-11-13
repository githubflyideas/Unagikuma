package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/engine"
)

// The external heartbeat is a GET of a URL, or a PUSH (a POST) whose body is
// one JSON object or one line of text. Either can carry a few facts about
// this machine; a GET carries them as URL parameters. Credentials are
// filled in from fields the way an API client does it: a user name and
// password (basic authentication) or an API key in a header or a URL
// parameter.

// HeartbeatKeys are the settings of the external heartbeat.
var HeartbeatKeys = []string{"heartbeat_url", "heartbeat_method", "heartbeat_format", "heartbeat_host_info",
	"heartbeat_auth", "heartbeat_user", "heartbeat_password", "heartbeat_key_name", "heartbeat_key_value", "heartbeat_key_in"}

// HeartbeatSecretKeys are shown masked and kept when "***" comes back.
var HeartbeatSecretKeys = map[string]bool{"heartbeat_password": true, "heartbeat_key_value": true}

var heartbeatChoices = map[string][]string{
	"heartbeat_method":    {"", "GET", "PUSH"},
	"heartbeat_format":    {"", "json", "text"},
	"heartbeat_host_info": {"", "on", "off"},
	"heartbeat_auth":      {"", "none", "basic", "apikey"},
	"heartbeat_key_in":    {"", "header", "query"},
}

func isHeartbeatKey(key string) bool {
	for _, k := range HeartbeatKeys {
		if k == key {
			return true
		}
	}
	return false
}

// checkHeartbeatSetting validates one of the heartbeat settings.
func checkHeartbeatSetting(key, value string) error {
	if choices, ok := heartbeatChoices[key]; ok {
		for _, c := range choices {
			if value == c {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %s", key, strings.Join(choices[1:], ", "))
	}
	if key == "heartbeat_key_name" && strings.ContainsAny(value, " \t\r\n:") {
		return fmt.Errorf("the API key name must be one word")
	}
	return nil
}

// HeartbeatInfo is what a heartbeat says about this machine.
type HeartbeatInfo struct {
	Host     string `json:"host"`
	Status   string `json:"status"` // ok or problems
	Problems int    `json:"problems"`
	Monitors int    `json:"monitors"`
	Time     string `json:"time"`
}

func (a *App) heartbeatInfo() HeartbeatInfo {
	in := HeartbeatInfo{Host: a.instance(), Status: "ok", Time: a.now().UTC().Format(time.RFC3339)}
	if a.engine != nil {
		ov := a.engine.Overview()
		for _, c := range ov.Counts {
			in.Monitors += c
		}
		in.Problems = ov.Unhandled
		if ov.Unhandled > 0 {
			in.Status = "problems"
		}
	}
	return in
}

// buildHeartbeat makes the call from the settings (get reads one) and the
// facts about this machine.
func buildHeartbeat(get func(string) string, raw string, in HeartbeatInfo) (engine.HeartbeatRequest, error) {
	hr := engine.HeartbeatRequest{Method: http.MethodGet, Header: http.Header{}}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return hr, fmt.Errorf("invalid heartbeat URL")
	}
	q, changed := u.Query(), false
	withInfo := get("heartbeat_host_info") != "off"
	push := get("heartbeat_method") == "PUSH"
	if push {
		hr.Method = http.MethodPost
		if get("heartbeat_format") == "text" {
			hr.Header.Set("Content-Type", "text/plain; charset=utf-8")
			hr.Body = fmt.Sprintf("%s %s %d problems %d monitors %s", in.Host, in.Status, in.Problems, in.Monitors, in.Time)
		} else {
			hr.Header.Set("Content-Type", "application/json")
			b, _ := json.Marshal(in)
			hr.Body = string(b)
		}
	} else if withInfo {
		// A parameter the URL already has is the receiver's own (an Uptime
		// Kuma push URL brings status=up): it is kept as it is.
		for k, v := range map[string]string{"host": in.Host, "status": in.Status, "problems": fmt.Sprint(in.Problems),
			"monitors": fmt.Sprint(in.Monitors), "time": in.Time} {
			if !q.Has(k) {
				q.Set(k, v)
				changed = true
			}
		}
	}
	switch get("heartbeat_auth") {
	case "basic":
		hr.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(get("heartbeat_user")+":"+get("heartbeat_password"))))
	case "apikey":
		name := get("heartbeat_key_name")
		if name == "" {
			name = "X-Api-Key"
		}
		if get("heartbeat_key_in") == "query" {
			q.Set(name, get("heartbeat_key_value"))
			changed = true
		} else {
			hr.Header.Set(name, get("heartbeat_key_value"))
		}
	}
	if changed { // otherwise the URL goes out exactly as written
		u.RawQuery = q.Encode()
	}
	hr.URL = u.String()
	return hr, nil
}

// prepareHeartbeat builds the call from the saved settings.
func (a *App) prepareHeartbeat(raw string) (engine.HeartbeatRequest, error) {
	return buildHeartbeat(a.Setting, raw, a.heartbeatInfo())
}

// PreviewHeartbeat builds the call from settings that are not saved yet
// (the form), with credentials masked. A secret sent back as "***" is the
// saved one.
func (a *App) PreviewHeartbeat(form map[string]string) (engine.HeartbeatRequest, error) {
	get := func(k string) string {
		v := form[k]
		if HeartbeatSecretKeys[k] && v == "***" {
			v = a.Setting(k)
		}
		return v
	}
	for k, v := range form {
		if err := checkHeartbeatSetting(k, v); err != nil {
			return engine.HeartbeatRequest{}, err
		}
	}
	hr, err := buildHeartbeat(get, form["heartbeat_url"], a.heartbeatInfo())
	if err != nil {
		return hr, err
	}
	// Show where the credentials go, not what they are.
	for _, k := range []string{"Authorization", get("heartbeat_key_name"), "X-Api-Key"} {
		if v := hr.Header.Get(k); v != "" && k != "" {
			if scheme, _, ok := strings.Cut(v, " "); ok && k == "Authorization" {
				hr.Header.Set(k, scheme+" ***")
			} else {
				hr.Header.Set(k, "***")
			}
		}
	}
	if get("heartbeat_auth") == "apikey" && get("heartbeat_key_in") == "query" {
		if u, err := url.Parse(hr.URL); err == nil {
			q := u.Query()
			name := get("heartbeat_key_name")
			if name == "" {
				name = "X-Api-Key"
			}
			q.Set(name, "MASKED")
			u.RawQuery = q.Encode()
			hr.URL = strings.Replace(u.String(), "MASKED", "***", 1)
		}
	}
	return hr, nil
}
