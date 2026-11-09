package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// checkFunc decides success from the HTTP status and body (many bot APIs
// answer 200 with an error code in the body).
type checkFunc func(status int, body []byte) error

func status2xx(status int, _ []byte) error {
	if status < 200 || status > 299 {
		return fmt.Errorf("HTTP %d", status)
	}
	return nil
}

// jsonCode fails when any of the fields is present and not equal to ok.
func jsonCode(ok string, fields ...string) checkFunc {
	return func(status int, body []byte) error {
		if err := status2xx(status, body); err != nil {
			return err
		}
		var m map[string]any
		if json.Unmarshal(body, &m) != nil {
			return fmt.Errorf("unexpected response (not JSON)")
		}
		for _, f := range fields {
			if v, has := m[f]; has && fmt.Sprint(v) != ok {
				msg := m["msg"]
				if msg == nil {
					msg = m["errmsg"]
				}
				return fmt.Errorf("%s=%v %v", f, v, msg)
			}
		}
		return nil
	}
}

func (s *Sender) newRequest(ctx context.Context, method, rawURL, contentType string, body []byte, m *Message) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid URL")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	ua := "unagikuma"
	if s.Instance != "" {
		ua += "/" + s.Instance
	}
	req.Header.Set("User-Agent", ua)
	if m.DeliveryID != "" {
		req.Header.Set("X-Unagikuma-Delivery-Id", m.DeliveryID)
	}
	return req, nil
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

const ctJSON = "application/json; charset=utf-8"

func (s *Sender) build(ctx context.Context, ch Channel, m *Message) (*http.Request, checkFunc, error) {
	o := ch.Opts
	title, body := Render(m)
	full := title
	if body != "" {
		full += "\n" + body
	}
	switch ch.Kind {
	case "feishu":
		payload := map[string]any{"msg_type": "text", "content": map[string]string{"text": full}}
		if sec := o["secret"]; sec != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			payload["timestamp"], payload["sign"] = ts, feishuSign(ts, sec)
		}
		req, err := s.newRequest(ctx, http.MethodPost, o["url"], ctJSON, jsonBody(payload), m)
		return req, jsonCode("0", "code", "StatusCode"), err
	case "dingtalk":
		u := o["url"]
		if sec := o["secret"]; sec != "" {
			ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
			sep := "&"
			if !strings.Contains(u, "?") {
				sep = "?"
			}
			u += sep + "timestamp=" + ts + "&sign=" + url.QueryEscape(dingtalkSign(ts, sec))
		}
		req, err := s.newRequest(ctx, http.MethodPost, u, ctJSON, jsonBody(map[string]any{"msgtype": "text", "text": map[string]string{"content": full}}), m)
		return req, jsonCode("0", "errcode"), err
	case "wecom":
		req, err := s.newRequest(ctx, http.MethodPost, o["url"], ctJSON, jsonBody(map[string]any{"msgtype": "text", "text": map[string]string{"content": full}}), m)
		return req, jsonCode("0", "errcode"), err
	case "slack":
		req, err := s.newRequest(ctx, http.MethodPost, o["url"], ctJSON, jsonBody(map[string]string{"text": full}), m)
		return req, status2xx, err
	case "discord":
		if len([]rune(full)) > 2000 {
			full = string([]rune(full)[:1999]) + "…"
		}
		req, err := s.newRequest(ctx, http.MethodPost, o["url"], ctJSON, jsonBody(map[string]string{"content": full}), m)
		return req, status2xx, err
	case "telegram":
		api := strings.TrimRight(o["api"], "/")
		if api == "" {
			api = "https://api.telegram.org"
		}
		req, err := s.newRequest(ctx, http.MethodPost, api+"/bot"+o["token"]+"/sendMessage", ctJSON,
			jsonBody(map[string]any{"chat_id": o["chat"], "text": full, "disable_web_page_preview": true}), m)
		return req, jsonCode("true", "ok"), err
	case "webhook":
		if p := o["preset"]; p != "" {
			return s.buildPreset(ctx, presets[p], o, m)
		}
		return s.buildWebhook(ctx, o, m)
	}
	return nil, nil, fmt.Errorf("unknown channel kind %q", ch.Kind)
}

// feishuSign: HMAC-SHA256 keyed by "timestamp\nsecret" over an empty
// message, base64 (Feishu custom bot signature).
func feishuSign(ts, secret string) string {
	h := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// dingtalkSign: HMAC-SHA256 keyed by the secret over "timestamp\nsecret".
func dingtalkSign(ts, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ts + "\n" + secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// buildWebhook sends the message as JSON, or renders the configured body
// template.
func (s *Sender) buildWebhook(ctx context.Context, o map[string]string, m *Message) (*http.Request, checkFunc, error) {
	method := strings.ToUpper(o["method"])
	if method == "" {
		method = http.MethodPost
	}
	ct := o["content-type"]
	if ct == "" {
		ct = "json"
	}
	var body []byte
	if tpl := o["body"]; tpl != "" {
		out, err := renderTemplate(tpl, templateData(m, o, ct == "json"))
		if err != nil {
			return nil, nil, err
		}
		body = []byte(out)
	} else {
		title, text := Render(m)
		payload := map[string]any{
			"title": title, "text": text, "kind": m.Kind, "priority": m.Priority, "monitor": m.Monitor,
			"display_name": m.Title, "type": m.Type, "target": m.Target, "depth": m.Depth, "status": m.Status,
			"reason": m.Reason, "class": m.Class, "detail": sanitize(m.Detail, detailCap), "quorum": m.Quorum, "count": m.Count, "max": m.Max,
			"incident_id": m.IncidentID, "incident_global_id": m.GlobalIncidentID, "observations": m.Observations, "duration_s": int(m.Duration.Seconds()), "detail_url": m.DetailURL,
			"tags": append([]string{}, m.Tags...), "time": m.Time, "delayed_s": int(m.Delayed.Seconds()), "drill": m.DryRun,
			"instance": m.Instance, "site": m.Site, "delivery_id": m.DeliveryID,
		}
		body = jsonBody(payload)
		ct = "json"
	}
	contentType := map[string]string{"json": ctJSON, "form": "application/x-www-form-urlencoded", "text": "text/plain; charset=utf-8"}[ct]
	u := o["url"]
	if method == http.MethodGet {
		body = nil
	}
	req, err := s.newRequest(ctx, method, u, contentType, body, m)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range o {
		if name, ok := strings.CutPrefix(k, "header."); ok {
			req.Header.Set(name, v)
		}
	}
	match := o["success-match"]
	return req, func(status int, b []byte) error {
		if err := status2xx(status, b); err != nil {
			return err
		}
		if match != "" && !strings.Contains(string(b), match) {
			return fmt.Errorf("response does not contain %q", match)
		}
		return nil
	}, nil
}

// respCap bounds how much of a response is read.
const respCap = 64 << 10

func (s *Sender) do(req *http.Request, check checkFunc, a Attempt) Attempt {
	resp, err := s.client().Do(req)
	if err != nil {
		a.Error = shortNetError(err)
		return a
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, respCap))
	a.Status = resp.StatusCode
	a.Response = string(body)
	if err := check(resp.StatusCode, body); err != nil {
		a.Error = err.Error()
		return a
	}
	a.OK = true
	return a
}

func shortNetError(err error) string {
	var ue *url.Error
	if e, ok := err.(*url.Error); ok {
		ue = e
	}
	switch {
	case ue != nil && ue.Timeout():
		return "timeout"
	case ue != nil:
		return ue.Err.Error()
	}
	return err.Error()
}
