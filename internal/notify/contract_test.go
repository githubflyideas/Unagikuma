package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// The tests here hold each channel kind to its service's rules: what the
// request must look like, how long a message may be, and what the
// service's replies mean. The replies for wrong credentials were recorded
// from the real services.

// seen is one request a stand-in received.
type seen struct {
	method, path, query string
	header              http.Header
	body                []byte
}

// standIn answers every request with a fixed reply and keeps the requests.
// Requests to any host are sent to it, so presets with a fixed address
// can be tested.
type standIn struct {
	mu     sync.Mutex
	reqs   []seen
	status int
	reply  string
	header map[string]string
	srv    *httptest.Server
}

func newStandIn(t *testing.T) *standIn {
	s := &standIn{status: 200}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), b})
		status, reply, hdr := s.status, s.reply, s.header
		s.mu.Unlock()
		if reply == "" {
			reply = okReply(r.URL.Path)
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// okReply is what each service answers when it took the message.
func okReply(path string) string {
	switch {
	case path == "/1/messages.json":
		return `{"status":1,"request":"r"}` // Pushover
	case path == "/push" || path == "/send":
		return `{"code":200,"msg":"ok"}` // Bark, PushPlus
	case strings.HasSuffix(path, ".send"):
		return `{"code":0,"message":""}` // ServerChan
	case path == "/v3/sms/send":
		return `{"http_code":200,"response_code":"SUCCESS","data":{"messages":[{"status":"SUCCESS"},{"status":"SUCCESS"}]}}` // ClickSend
	}
	return `{"ok":true,"code":0,"errcode":0,"status":"ok","StatusCode":0}`
}

func (s *standIn) sender() *Sender {
	target, _ := url.Parse(s.srv.URL)
	return &Sender{Client: &http.Client{Transport: toStandIn{target}, CheckRedirect: noRedirect}}
}

func (s *standIn) last(t *testing.T) seen {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		t.Fatal("no request was made")
	}
	return s.reqs[len(s.reqs)-1]
}

// toStandIn sends every request to one server, keeping path and query.
type toStandIn struct{ to *url.URL }

func (r toStandIn) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	c.URL.Scheme, c.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(c)
}

func aProblem(lang string) *Message {
	return &Message{Kind: "PROBLEM", Priority: "high", Monitor: "pay", Title: `支付网关 "API" <gw> & co`, Type: "HTTP", Target: "https://pay.example.com/health",
		Depth: "L3", Status: "critical", Reason: "http_status", Detail: "HTTP 503, expected 200-399", Count: 1, Max: 3, IncidentID: 7, GlobalIncidentID: "a:b:7",
		Duration: 90 * time.Second, DetailURL: "https://mon.example.com/#/monitors/pay", Tags: []string{"env:prod"}, Time: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC),
		Instance: "hq", DeliveryID: "0123456789abcdef0123456789abcdef", Lang: lang, Class: "business"}
}

// report is the longest message the product sends: 120 lines.
func report(lang string) *Message {
	var b strings.Builder
	for i := 0; i < 120; i++ {
		b.WriteString("• 订单服务节点 order-service：故障，自 2026-11-12 08:00 起 — connection refused\n")
	}
	return &Message{Kind: "REPORT", Priority: "low", Title: "每日状态：31 个服务中 30 个异常", Body: b.String(), Time: time.Now(), Instance: "hq", DeliveryID: "r1", Lang: lang}
}

// contracts are every kind's options and how to read its message back.
var contracts = []struct {
	name, kind string
	opts       map[string]string
	// text reads the message out of the request; limit is the service's.
	text  func(seen) string
	limit limit
}{
	{"feishu", "feishu", map[string]string{"url": "https://open.feishu.cn/open-apis/bot/v2/hook/t"}, jsonField("content", "text"), limit{bytes: 20 * 1024}},
	{"dingtalk", "dingtalk", map[string]string{"url": "https://oapi.dingtalk.com/robot/send?access_token=t"}, jsonField("text", "content"), limit{bytes: 20000}},
	{"wecom", "wecom", map[string]string{"url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k"}, jsonField("text", "content"), limit{bytes: 2048}},
	{"telegram", "telegram", map[string]string{"token": "1:a", "chat": "1"}, jsonField("text"), limit{utf16: 4096}},
	{"slack", "slack", map[string]string{"url": "https://hooks.slack.com/services/a/b/c"}, jsonField("text"), limit{runes: 40000}},
	{"discord", "discord", map[string]string{"url": "https://discord.com/api/webhooks/1/t"}, jsonField("content"), limit{runes: 2000}},
	{"ntfy", "webhook", map[string]string{"preset": "ntfy", "topic": "a"}, func(s seen) string { return string(s.body) }, limit{bytes: 4096}},
	{"gotify", "webhook", map[string]string{"preset": "gotify", "server": "https://g.example.com", "token": "t"}, jsonField("message"), limit{}},
	{"bark", "webhook", map[string]string{"preset": "bark", "device-key": "k"}, func(s seen) string { return string(s.body) }, limit{bytes: 4096}},
	{"pushover", "webhook", map[string]string{"preset": "pushover", "token": "t", "user-key": "u"}, jsonField("message"), limit{runes: 1024}},
	{"serverchan", "webhook", map[string]string{"preset": "serverchan", "sendkey": "k"}, jsonField("desp"), limit{}},
	{"pushplus", "webhook", map[string]string{"preset": "pushplus", "token": "t"}, jsonField("content"), limit{runes: 20000}},
	{"teams", "webhook", map[string]string{"preset": "teams", "url": "https://x.example.com/workflows/a"}, func(s seen) string { return string(s.body) }, limit{bytes: 28 * 1024}},
	{"matrix", "webhook", map[string]string{"preset": "matrix", "homeserver": "https://m.example.com", "token": "t", "room": "!r:example.com"}, jsonField("body"), limit{}},
	{"mattermost", "webhook", map[string]string{"preset": "mattermost", "url": "https://mm.example.com/hooks/x"}, jsonField("text"), limit{runes: 16383}},
	{"googlechat", "webhook", map[string]string{"preset": "googlechat", "url": "https://chat.googleapis.com/v1/spaces/x/messages?key=k"}, jsonField("text"), limit{runes: 4096}},
	{"pagerduty", "webhook", map[string]string{"preset": "pagerduty", "routing-key": "k"}, jsonField("payload", "summary"), limit{runes: 1024}},
	{"opsgenie", "webhook", map[string]string{"preset": "opsgenie", "api-key": "k"}, func(s seen) string { return jsonField("description")(s) + jsonField("note")(s) }, limit{runes: 15000}},
	{"pushbullet", "webhook", map[string]string{"preset": "pushbullet", "token": "t"}, jsonField("body"), limit{}},
	{"line", "webhook", map[string]string{"preset": "line", "token": "t", "to": "U1"}, func(s seen) string {
		var d struct{ Messages []struct{ Text string } }
		_ = json.Unmarshal(s.body, &d)
		if len(d.Messages) == 0 {
			return ""
		}
		return d.Messages[0].Text
	}, limit{runes: 5000}},
	{"datadog", "webhook", map[string]string{"preset": "datadog", "api-key": "k"}, jsonField("text"), limit{runes: 4000}},
	{"apprise", "webhook", map[string]string{"preset": "apprise", "server": "http://apprise.example.com", "key": "cfg"}, jsonField("body"), limit{}},
	{"webhook", "webhook", map[string]string{"url": "https://hook.example.com/in"}, jsonField("text"), limit{}},
}

// jsonField reads a (nested) string field of a JSON body.
func jsonField(path ...string) func(seen) string {
	return func(s seen) string {
		var v any
		if json.Unmarshal(s.body, &v) != nil {
			return "\x00not JSON: " + string(s.body)
		}
		for _, k := range path {
			m, ok := v.(map[string]any)
			if !ok {
				return ""
			}
			v = m[k]
		}
		str, _ := v.(string)
		return str
	}
}

// Every kind sends every sort of message in a form its service takes: a
// body that parses, a text that is there, and no longer than allowed.
func TestEveryKindKeepsToItsServicesRules(t *testing.T) {
	for _, c := range contracts {
		for _, lang := range []string{"zh", "en"} {
			rec := aProblem(lang)
			rec.Kind = "RECOVERY"
			test := &Message{Kind: "TEST", Priority: "low", Title: "unagikuma test", Time: time.Now(), Instance: "hq", Lang: lang, DeliveryID: "t1"}
			for _, m := range []*Message{aProblem(lang), rec, report(lang), test} {
				s := newStandIn(t)
				a := s.sender().Send(context.Background(), Channel{Name: c.name, Kind: c.kind, Opts: c.opts}, m)
				if !a.OK {
					t.Errorf("%s %s %s: %s", c.name, lang, m.Kind, a.Error)
					continue
				}
				r := s.last(t)
				text := c.text(r)
				if strings.HasPrefix(text, "\x00") || strings.TrimSpace(text) == "" {
					t.Errorf("%s %s %s: no text in the request: %.200s", c.name, lang, m.Kind, text)
					continue
				}
				if !c.limit.fits(text) {
					t.Errorf("%s %s %s: %d bytes / %d characters / %d UTF-16 units, the service takes %+v", c.name, lang, m.Kind,
						len(text), utf8.RuneCountInString(text), len(utf16.Encode([]rune(text))), c.limit)
				}
				if ct := r.header.Get("Content-Type"); strings.Contains(ct, "json") && !json.Valid(r.body) {
					t.Errorf("%s %s %s: the body is not valid JSON: %.200s", c.name, lang, m.Kind, r.body)
				}
				if strings.Contains(string(r.body), "<no value>") {
					t.Errorf("%s %s %s: <no value> in the request", c.name, lang, m.Kind)
				}
			}
		}
	}
}

// A message cut to fit says so, and keeps whole lines.
func TestALongMessageIsCutAtALineAndSaysSo(t *testing.T) {
	s := newStandIn(t)
	a := s.sender().Send(context.Background(), Channel{Name: "w", Kind: "wecom", Opts: map[string]string{"url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k"}}, report("zh"))
	if !a.OK {
		t.Fatal(a.Error)
	}
	text := jsonField("text", "content")(s.last(t))
	lines := strings.Split(text, "\n")
	if len(text) > 2048 || !strings.Contains(lines[len(lines)-1], "已截断") {
		t.Fatalf("%d bytes, last line %q", len(text), lines[len(lines)-1])
	}
	for _, l := range lines[1 : len(lines)-1] {
		if !strings.HasSuffix(l, "connection refused") {
			t.Fatalf("a line was cut in the middle: %q", l)
		}
	}
	if got := cut(strings.Repeat("好", 100), limit{bytes: 30}); len(got) > 30 || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("cut: %q", got)
	}
}

// What the services answer, and what it means for the next step: try
// again, wait, or go on to the next channel.
func TestServiceRepliesAreUnderstood(t *testing.T) {
	feishu := Channel{Name: "c", Kind: "feishu", Opts: map[string]string{"url": "https://open.feishu.cn/open-apis/bot/v2/hook/t"}}
	ding := Channel{Name: "c", Kind: "dingtalk", Opts: map[string]string{"url": "https://oapi.dingtalk.com/robot/send?access_token=t"}}
	wecom := Channel{Name: "c", Kind: "wecom", Opts: map[string]string{"url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k"}}
	tg := Channel{Name: "c", Kind: "telegram", Opts: map[string]string{"token": "1:a", "chat": "1"}}
	slack := Channel{Name: "c", Kind: "slack", Opts: map[string]string{"url": "https://hooks.slack.com/services/a/b/c"}}
	discord := Channel{Name: "c", Kind: "discord", Opts: map[string]string{"url": "https://discord.com/api/webhooks/1/t"}}
	preset := func(name string, o map[string]string) Channel {
		o["preset"] = name
		return Channel{Name: "c", Kind: "webhook", Opts: o}
	}
	for _, c := range []struct {
		name   string
		ch     Channel
		status int
		reply  string
		header map[string]string
		ok     bool
		perm   bool
		wait   time.Duration
		says   string
	}{
		{"feishu ok", feishu, 200, `{"StatusCode":0,"StatusMessage":"success","code":0,"data":{},"msg":"success"}`, nil, true, false, 0, ""},
		{"feishu wrong token (recorded)", feishu, 200, `{"code":19001,"data":{},"msg":"param invalid: incoming webhook access token invalid"}`, nil, false, true, 0, "access token invalid"},
		{"feishu signature", feishu, 200, `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`, nil, false, true, 0, "sign match fail"},
		{"feishu rate limit", feishu, 200, `{"code":11232,"msg":"frequency limited"}`, nil, false, false, 10 * time.Second, "frequency limited"},
		{"dingtalk ok", ding, 200, `{"errcode":0,"errmsg":"ok"}`, nil, true, false, 0, ""},
		{"dingtalk wrong token (recorded)", ding, 200, `{"errcode":300005,"errmsg":"token is not exist"}`, nil, false, true, 0, "errcode=300005 token is not exist"},
		{"dingtalk too fast", ding, 200, `{"errcode":130101,"errmsg":"send too fast, exceed 20 times per minute"}`, nil, false, false, time.Minute, "send too fast"},
		{"wecom wrong key (recorded)", wecom, 200, `{"errcode":93000,"errmsg":"invalid webhook url, hint: [1], from ip: 1.2.3.4"}`, nil, false, true, 0, "invalid webhook url"},
		{"wecom too fast", wecom, 200, `{"errcode":45009,"errmsg":"api freq out of limit"}`, nil, false, false, time.Minute, "freq out of limit"},
		{"telegram wrong token (recorded)", tg, 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, nil, false, true, 0, "HTTP 401: Unauthorized"},
		{"telegram flood", tg, 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`, nil, false, false, 7 * time.Second, "Too Many Requests"},
		{"slack unknown hook (recorded)", slack, 404, `no_team`, nil, false, true, 0, "HTTP 404: no_team"},
		{"slack rate limit", slack, 429, `rate_limited`, map[string]string{"Retry-After": "3"}, false, false, 3 * time.Second, "HTTP 429"},
		{"discord unknown hook (recorded)", discord, 404, `{"message": "Unknown Webhook", "code": 10015}`, nil, false, true, 0, "Unknown Webhook"},
		{"discord rate limit", discord, 429, `{"message":"You are being rate limited.","retry_after":1.5,"global":false}`, nil, false, false, 1500 * time.Millisecond, "rate limited"},
		{"server error", slack, 503, `<html>upstream</html>`, nil, false, false, 0, "HTTP 503"},
		{"pushover wrong token (recorded)", preset("pushover", map[string]string{"token": "t", "user-key": "u"}), 400, `{"token":"invalid","errors":["application token is invalid, see https://pushover.net/api"],"status":0}`, nil, false, true, 0, "application token is invalid"},
		{"serverchan wrong key (recorded)", preset("serverchan", map[string]string{"sendkey": "k"}), 400, `{"message":"[AUTH]错误的Key","code":40001,"info":"错误的Key","scode":461}`, nil, false, true, 0, "错误的Key"},
		{"pushplus wrong token (recorded)", preset("pushplus", map[string]string{"token": "t"}), 200, `{"code":903,"data":"无效的用户token","msg":"用户令牌不正确"}`, nil, false, false, 0, "code=903 用户令牌不正确"},
		{"opsgenie wrong key (recorded)", preset("opsgenie", map[string]string{"api-key": "k"}), 401, `{"message":"Could not authenticate","took":0.001}`, nil, false, true, 0, "Could not authenticate"},
		{"bark unknown device (recorded)", preset("bark", map[string]string{"device-key": "k"}), 400, `{"code":400,"message":"failed to get device token","timestamp":1}`, nil, false, true, 0, "failed to get device token"},
		{"pushbullet wrong token (recorded)", preset("pushbullet", map[string]string{"token": "t"}), 401, `{"error":{"code":"invalid_access_token","type":"invalid_request","message":"Access token is missing or invalid."},"error_code":"invalid_access_token"}`, nil, false, true, 0, "Access token is missing or invalid"},
		{"line wrong token (recorded)", preset("line", map[string]string{"token": "t", "to": "U1"}), 401, `{"message":"Authentication failed. Confirm that the access token in the authorization header is valid."}`, nil, false, true, 0, "Authentication failed"},
		{"clicksend wrong key (recorded)", preset("clicksend", map[string]string{"username": "u", "api-key": "k", "to": "+61411111111"}), 401, `{"http_code":401,"response_code":"UNAUTHORIZED","response_msg":"Authorization failed.","data":null}`, nil, false, true, 0, "Authorization failed"},
		{"clicksend refused number", preset("clicksend", map[string]string{"username": "u", "api-key": "k", "to": "+61411111111"}), 200, `{"http_code":200,"response_code":"SUCCESS","response_msg":"Messages queued for delivery.","data":{"messages":[{"status":"INVALID_RECIPIENT"}]}}`, nil, false, false, 0, "INVALID_RECIPIENT"},
		{"datadog ok (recorded)", preset("datadog", map[string]string{"api-key": "k"}), 202, `{"status":"ok","event":{"id":1}}`, nil, true, false, 0, ""},
	} {
		s := newStandIn(t)
		s.status, s.reply, s.header = c.status, c.reply, c.header
		a := s.sender().Send(context.Background(), c.ch, aProblem("en"))
		if a.OK != c.ok || a.Permanent != c.perm || a.RetryAfter != c.wait || !strings.Contains(a.Error, c.says) {
			t.Errorf("%s: ok=%v permanent=%v wait=%s error=%q; want ok=%v permanent=%v wait=%s and %q", c.name, a.OK, a.Permanent, a.RetryAfter, a.Error, c.ok, c.perm, c.wait, c.says)
		}
	}
}

// SMS providers: the account or the template being wrong will not go
// away by sending again; a busy provider may.
func TestSMSProviderErrorsAreClassified(t *testing.T) {
	ali := Channel{Name: "c", Kind: "aliyun-sms", Opts: map[string]string{"access-key-id": "a", "access-key-secret": "b", "sign-name": "s", "template-code": "t", "phones": "13800000000"}}
	tc := Channel{Name: "c", Kind: "tencent-sms", Opts: map[string]string{"secret-id": "a", "secret-key": "b", "app-id": "1", "sign-name": "s", "template-id": "1", "phones": "+8613800000000"}}
	tw := Channel{Name: "c", Kind: "twilio-sms", Opts: map[string]string{"account-sid": "AC1", "auth-token": "t", "from": "+1555", "phones": "+15550100"}}
	for _, c := range []struct {
		name   string
		ch     Channel
		status int
		reply  string
		perm   bool
		says   string
	}{
		{"aliyun wrong key (recorded)", ali, 404, `{"Message":"Specified access key is not found.","Code":"InvalidAccessKeyId.NotFound"}`, true, "InvalidAccessKeyId.NotFound"},
		{"aliyun template", ali, 200, `{"Code":"isv.SMS_TEMPLATE_ILLEGAL","Message":"模板不合法"}`, true, "isv.SMS_TEMPLATE_ILLEGAL"},
		{"aliyun busy", ali, 200, `{"Code":"isp.SYSTEM_ERROR","Message":"系统错误"}`, false, "isp.SYSTEM_ERROR"},
		{"tencent wrong id (recorded)", tc, 200, `{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"The SecretId is not found."},"RequestId":"r"}}`, true, "AuthFailure.SecretIdNotFound"},
		{"tencent busy", tc, 200, `{"Response":{"Error":{"Code":"RequestLimitExceeded","Message":"busy"}}}`, false, "RequestLimitExceeded"},
		{"twilio wrong account (recorded)", tw, 401, `{"code":20003,"message":"auth account AC1 does not exist","status":401}`, true, "does not exist"},
		{"twilio busy", tw, 503, `{"code":20503,"message":"Service unavailable","status":503}`, false, "Service unavailable"},
	} {
		s := newStandIn(t)
		s.status, s.reply = c.status, c.reply
		a := s.sender().Send(context.Background(), c.ch, aProblem("en"))
		if a.OK || a.Permanent != c.perm || !strings.Contains(a.Error, c.says) {
			t.Errorf("%s: ok=%v permanent=%v error=%q; want permanent=%v and %q", c.name, a.OK, a.Permanent, a.Error, c.perm, c.says)
		}
	}
}

// template= sets the text of problems and recoveries; a summary or a
// report keeps its own; a test shows the template at work.
func TestTemplateSetsTheMessageText(t *testing.T) {
	tpl := "{{.Icon}} {{.KindText}}：{{.Name}} {{.StatusText}}\n原因：{{.ReasonText}} — {{.Detail}}\n持续 {{.DurationText}}，负责人 {{.owner}}{{.Nobody}}\n{{.DetailURL}}"
	for _, c := range contracts {
		if c.name == "pagerduty" || c.name == "opsgenie" || c.name == "webhook" {
			continue // carry the title, or their own fields
		}
		o := map[string]string{"template": tpl}
		for k, v := range c.opts {
			o[k] = v
		}
		s := newStandIn(t)
		a := s.sender().Send(context.Background(), Channel{Name: c.name, Kind: c.kind, Opts: o}, aProblem("zh"))
		if !a.OK {
			t.Errorf("%s: %s", c.name, a.Error)
			continue
		}
		all := string(s.last(t).body) + s.last(t).header.Get("Title")
		// The body may be JSON: compare what is in it, not how it is quoted.
		for _, want := range []string{"故障", "支付网关", "HTTP 503", "持续 2分", "https://mon.example.com/#/monitors/pay"} {
			if !strings.Contains(all, want) {
				t.Errorf("%s: %q is not in the message: %.300s", c.name, want, all)
			}
		}
		if strings.Contains(all, "检查：") || strings.Contains(all, "no value") {
			t.Errorf("%s: the built-in text or a missing name is in the message: %.300s", c.name, all)
		}
	}
	// A report is not a message about one monitor: the template stays out.
	s := newStandIn(t)
	ch := Channel{Name: "f", Kind: "feishu", Opts: map[string]string{"url": "https://open.feishu.cn/open-apis/bot/v2/hook/t", "template": tpl}}
	s.sender().Send(context.Background(), ch, report("zh"))
	if text := jsonField("content", "text")(s.last(t)); !strings.Contains(text, "每日状态") || strings.Contains(text, "负责人") {
		t.Errorf("report with a template: %.200s", text)
	}
	// A test shows the template with made-up values.
	s.sender().Send(context.Background(), ch, &Message{Kind: "TEST", Title: "unagikuma test", Lang: "zh", Time: time.Now()})
	if text := jsonField("content", "text")(s.last(t)); !strings.Contains(text, "Example service") || !strings.Contains(text, "原因：") {
		t.Errorf("test with a template: %.300s", text)
	}
	// A template that fails never costs the alert.
	ch.Opts["template"] = "{{.Name"
	if a := s.sender().Send(context.Background(), ch, aProblem("zh")); !a.OK {
		t.Fatalf("broken template: %s", a.Error)
	}
	if text := jsonField("content", "text")(s.last(t)); !strings.Contains(text, "检查：") {
		t.Errorf("broken template did not fall back to the built-in text: %.200s", text)
	}
}

func TestTemplateProblemsAreNamed(t *testing.T) {
	for _, c := range []struct {
		kind string
		opts map[string]string
		want string
	}{
		{"feishu", map[string]string{"url": "https://x.example.com/h", "template": "{{.Name}} {{.StatusText}} {{.url}}"}, ""},
		{"feishu", map[string]string{"template": "{{.Owner}}"}, "template: unknown name Owner"},
		{"feishu", map[string]string{"template": "{{.Name"}, "template: unclosed action"},
		{"webhook", map[string]string{"url": "https://x.example.com/?m={{.Monitor}}", "body": `{"a":"{{.Title}}"}`}, ""},
		{"webhook", map[string]string{"url": "https://x.example.com/?m={{.Monitr}}"}, "url: unknown name Monitr"},
		{"aliyun-sms", map[string]string{"params": "{{.Name}}|{{.StatusText}}|{{.ReasonText}}"}, ""},
		{"aliyun-sms", map[string]string{"params": "{{.Nme}}"}, "params: unknown name Nme"},
		{"feishu", map[string]string{"template": "${secret:tpl}"}, ""},
	} {
		got := strings.Join(TemplateProblems(c.kind, c.opts), "; ")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %v: %q, want %q", c.kind, c.opts, got, c.want)
		}
	}
}

// at= mentions people on problems, not on recoveries.
func TestMentions(t *testing.T) {
	s := newStandIn(t)
	send := func(kind, hook, at string, m *Message) map[string]any {
		s.sender().Send(context.Background(), Channel{Name: "c", Kind: kind, Opts: map[string]string{"url": hook, "at": at}}, m)
		var d map[string]any
		_ = json.Unmarshal(s.last(t).body, &d)
		return d
	}
	rec := aProblem("zh")
	rec.Kind = "RECOVERY"
	d := send("dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=t", "13800000000, 13900000000", aProblem("zh"))
	if at, _ := d["at"].(map[string]any); at == nil || len(at["atMobiles"].([]any)) != 2 {
		t.Errorf("dingtalk mobiles: %v", d["at"])
	}
	d = send("dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=t", "all", aProblem("zh"))
	if at, _ := d["at"].(map[string]any); at == nil || at["isAtAll"] != true {
		t.Errorf("dingtalk all: %v", d["at"])
	}
	if d = send("dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=t", "all", rec); d["at"] != nil {
		t.Errorf("a recovery mentions: %v", d["at"])
	}
	d = send("wecom", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k", "all", aProblem("zh"))
	if l, _ := d["text"].(map[string]any)["mentioned_mobile_list"].([]any); len(l) != 1 || l[0] != "@all" {
		t.Errorf("wecom all: %v", d["text"])
	}
	d = send("feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/t", "all", aProblem("zh"))
	if text := d["content"].(map[string]any)["text"].(string); !strings.Contains(text, `<at user_id="all"></at>`) {
		t.Errorf("feishu all: %s", text)
	}
}

// A call is read in the message language, without signs a voice cannot say.
func TestTwilioCallScript(t *testing.T) {
	got := twiml("🔴 [Unagikuma] 故障：支付网关（pay）故障 & <x>", "zh")
	if !strings.Contains(got, `<Say language="zh-CN">Unagikuma 故障：支付网关 pay 故障 &amp; &lt;x&gt;</Say>`) || strings.ContainsAny(got, "🔴[]") {
		t.Fatalf("call script: %s", got)
	}
	if got := twiml("Problem", "xx"); !strings.Contains(got, "<Say>Problem</Say>") {
		t.Fatalf("unknown language: %s", got)
	}
}

// The requests of the presets added for Pushbullet, LINE, ClickSend,
// Datadog and Apprise, as their documentation asks for them.
func TestNewPresetRequests(t *testing.T) {
	s := newStandIn(t)
	send := func(name string, o map[string]string, m *Message) seen {
		t.Helper()
		o["preset"] = name
		if a := s.sender().Send(context.Background(), Channel{Name: name, Kind: "webhook", Opts: o}, m); !a.OK {
			t.Fatalf("%s: %s", name, a.Error)
		}
		return s.last(t)
	}
	var d map[string]any
	r := send("pushbullet", map[string]string{"token": "o.tok"}, aProblem("en"))
	_ = json.Unmarshal(r.body, &d)
	if r.path != "/v2/pushes" || r.header.Get("Access-Token") != "o.tok" || d["type"] != "note" || d["title"] == "" || d["body"] == "" {
		t.Errorf("pushbullet: %s %v %s", r.path, r.header, r.body)
	}
	r = send("line", map[string]string{"token": "tok", "to": "Uabc"}, aProblem("en"))
	_ = json.Unmarshal(r.body, &d)
	if r.path != "/v2/bot/message/push" || r.header.Get("Authorization") != "Bearer tok" || d["to"] != "Uabc" || d["messages"].([]any)[0].(map[string]any)["type"] != "text" {
		t.Errorf("line: %s %v %s", r.path, r.header, r.body)
	}
	r = send("clicksend", map[string]string{"username": "me", "api-key": "key-1", "to": "+61411111111, +8613800000000", "from": "Ops"}, aProblem("en"))
	_ = json.Unmarshal(r.body, &d)
	msgs, _ := d["messages"].([]any)
	if r.path != "/v3/sms/send" || r.header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("me:key-1")) || len(msgs) != 2 ||
		msgs[1].(map[string]any)["to"] != "+8613800000000" || msgs[0].(map[string]any)["from"] != "Ops" || msgs[0].(map[string]any)["body"] == "" {
		t.Errorf("clicksend: %s %v %s", r.path, r.header, r.body)
	}
	r = send("datadog", map[string]string{"api-key": "dd", "site": "datadoghq.eu"}, aProblem("en"))
	_ = json.Unmarshal(r.body, &d)
	if r.path != "/api/v1/events" || r.header.Get("Dd-Api-Key") != "dd" || d["alert_type"] != "error" || d["aggregation_key"] != "unagikuma-a:b:7" || d["tags"].([]any)[0] != "monitor:pay" {
		t.Errorf("datadog: %s %v %s", r.path, r.header, r.body)
	}
	rec := aProblem("en")
	rec.Kind = "RECOVERY"
	r = send("datadog", map[string]string{"api-key": "dd"}, rec)
	_ = json.Unmarshal(r.body, &d)
	if d["alert_type"] != "success" || d["aggregation_key"] != "unagikuma-a:b:7" {
		t.Errorf("datadog recovery: %s", r.body)
	}
	r = send("apprise", map[string]string{"server": "http://apprise.example.com:8000/", "key": "my cfg", "tag": "ops"}, aProblem("en"))
	_ = json.Unmarshal(r.body, &d)
	if r.path != "/notify/my cfg" || d["type"] != "failure" || d["tag"] != "ops" || d["title"] == "" || d["body"] == "" {
		t.Errorf("apprise: %q %s", r.path, r.body)
	}
}

// A webhook with method=GET has only its URL to carry anything.
func TestWebhookURLHoldsNames(t *testing.T) {
	s := newStandIn(t)
	a := s.sender().Send(context.Background(), Channel{Name: "g", Kind: "webhook", Opts: map[string]string{"url": "https://hook.example.com/in?m={{urlquery .Name}}&s={{.Status}}", "method": "GET"}}, aProblem("en"))
	r := s.last(t)
	if !a.OK || r.method != "GET" || r.query != "m="+url.QueryEscape(`支付网关 "API" <gw> & co`)+"&s=critical" {
		t.Fatalf("%v %s ?%s", a, r.method, r.query)
	}
}

// A template is one line in the file: \n between {{ }} parts is a line
// break, inside them it is left alone.
func TestTemplateLineBreaks(t *testing.T) {
	if got := templateLines(`a\nb {{join "\n" .Tags}} c\n`); got != "a\nb {{join \"\\n\" .Tags}} c\n" {
		t.Fatalf("%q", got)
	}
	title, body := textOf(map[string]string{"template": `{{.Name}}\n{{.StatusText}}\n{{join "\n" .Tags}}`}, aProblem("en"))
	if title != `支付网关 "API" <gw> & co` || body != "CRITICAL\nenv:prod" {
		t.Fatalf("title %q body %q", title, body)
	}
}

// A custom SMS gateway gets one request per number, with the number and
// the message where its templates put them; one number failing does not
// stop the others.
func TestCustomSMSGateway(t *testing.T) {
	s := newStandIn(t)
	ch := Channel{Name: "sms", Kind: "custom-sms", Opts: map[string]string{"url": "https://sms.example.com/send?to={{.Phone}}", "phones": "13800000000, 13900000000",
		"header.X-Key": "k1", "success-match": `"ok":true`}}
	s.reply = `{"ok":true}`
	if err := ValidateChannel(ch.Kind, ch.Opts); err != nil {
		t.Fatal(err)
	}
	if a := s.sender().Send(context.Background(), ch, aProblem("zh")); !a.OK {
		t.Fatal(a.Error)
	}
	if len(s.reqs) != 2 || s.reqs[1].query != "to=13900000000" || s.reqs[1].header.Get("X-Key") != "k1" {
		t.Fatalf("%d requests, last ?%s %v", len(s.reqs), s.reqs[1].query, s.reqs[1].header)
	}
	var d map[string]string
	if json.Unmarshal(s.reqs[0].body, &d) != nil || d["phone"] != "13800000000" || !strings.Contains(d["message"], "支付网关") {
		t.Fatalf("default body %s", s.reqs[0].body)
	}
	// A body of the operator's own, as a form.
	ch.Opts["content-type"], ch.Opts["body"] = "form", "mobile={{.Phone}}&text={{urlquery .Name}}"
	ch.Opts["url"] = "https://sms.example.com/send"
	s.sender().Send(context.Background(), ch, aProblem("zh"))
	if got := string(s.last(t).body); got != "mobile=13900000000&text="+url.QueryEscape(`支付网关 "API" <gw> & co`) {
		t.Fatalf("form body %q", got)
	}
	// The gateway refuses: the reason is reported, per number.
	s.status, s.reply = 403, `{"ok":false,"msg":"bad key"}`
	a := s.sender().Send(context.Background(), Channel{Name: "v", Kind: "custom-voice", Opts: map[string]string{"url": "https://v.example.com/call", "phones": "13800000000"}}, aProblem("zh"))
	if a.OK || !a.Permanent || !strings.Contains(a.Error, "13800000000: HTTP 403: bad key") {
		t.Fatalf("%+v", a)
	}
	if p := TemplateProblems("custom-sms", map[string]string{"url": "https://x/?p={{.Phone}}", "body": "{{.Phonee}}"}); len(p) != 1 || !strings.Contains(p[0], "Phonee") {
		t.Fatalf("problems %v", p)
	}
}
