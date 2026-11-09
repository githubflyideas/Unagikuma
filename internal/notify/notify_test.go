package notify

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func problem() *Message {
	return &Message{
		Kind: "PROBLEM", Priority: "high", Monitor: "core-ntp", Title: "核心 NTP", Type: "UDP", Target: "10.0.0.1:ntp", Depth: "L3",
		Status: "critical", Reason: "assert_failed", Detail: "offset 1.8s \"quoted\"\n}{ breaker\x07", Count: 1, Max: 3, IncidentID: 42, GlobalIncidentID: "home:epoch-test:42",
		Duration: 2 * time.Minute, Time: time.Date(2026, 10, 21, 14, 5, 20, 0, time.UTC), DetailURL: "https://mon.example.com/#/monitors/core-ntp",
		Lang: "zh", DeliveryID: "d-123", Instance: "home",
	}
}

func TestRenderZhEn(t *testing.T) {
	m := problem()
	title, body := Render(m)
	for _, want := range []string{"🔴", "故障", "核心 NTP", "core-ntp", "第 1/3 次通知"} {
		if !strings.Contains(title, want) {
			t.Errorf("zh title %q lacks %q", title, want)
		}
	}
	for _, want := range []string{"检查：UDP 10.0.0.1:ntp（L3）", "原因：响应内容不符合预期", "开始：2026-10-21 14:03:20（已持续 2分）", "详情：https://mon.example.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("zh body lacks %q:\n%s", want, body)
		}
	}
	if strings.ContainsRune(body, '\x07') {
		t.Error("control characters must be stripped")
	}
	m.Lang = "en"
	m.Kind, m.Status, m.Body = "RECOVERY", "ok", ""
	title, body = Render(m)
	if !strings.Contains(title, "Recovered") || !strings.Contains(body, "Was down for 2m; 1 notification sent") {
		t.Errorf("en recovery: %q / %q", title, body)
	}
	m.Title, m.Kind, m.Status = "core-ntp", "PROBLEM", "critical"
	if title, _ = Render(m); strings.Contains(title, "(core-ntp)") || !strings.Contains(title, "Problem: core-ntp") {
		t.Errorf("a monitor without its own title is named once: %q", title)
	}
	m.Lang, m.Detail = "zh", "connection refused"
	if _, body = Render(m); !strings.Contains(body, "连接被拒绝") {
		t.Errorf("zh detail not translated:\n%s", body)
	}
	m.Lang = "en"
	m.Kind, m.Delayed, m.DryRun = "REPEAT", 12*time.Minute, true
	if title, _ = Render(m); !strings.Contains(title, "[delayed 12 min] [DRILL]") {
		t.Errorf("prefixes: %q", title)
	}
}

// capture is an HTTP endpoint recording requests.
type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
	body [][]byte
	resp string
	code int
}

func (c *capture) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs, c.body = append(c.reqs, r), append(c.body, b)
		resp, code := c.resp, c.code
		c.mu.Unlock()
		if code == 0 {
			code = 200
		}
		w.WriteHeader(code)
		io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (c *capture) last() (*http.Request, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs[len(c.reqs)-1], c.body[len(c.body)-1]
}

func send(t *testing.T, kind string, opts map[string]string, m *Message) Attempt {
	t.Helper()
	s := &Sender{Instance: "home"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.Send(ctx, Channel{Name: "c", Kind: kind, Opts: opts}, m)
}

func TestNativeChannels(t *testing.T) {
	cases := []struct {
		kind       string
		okBody     string
		failBody   string
		bodyFields []string
	}{
		{"feishu", `{"code":0,"msg":"success"}`, `{"code":19001,"msg":"param invalid"}`, []string{"msg_type", "content"}},
		{"dingtalk", `{"errcode":0,"errmsg":"ok"}`, `{"errcode":310000,"errmsg":"sign not match"}`, []string{"msgtype", "text"}},
		{"wecom", `{"errcode":0}`, `{"errcode":93000}`, []string{"msgtype", "text"}},
		{"slack", `ok`, ``, []string{"text"}},
		{"discord", ``, ``, []string{"content"}},
	}
	for _, c := range cases {
		cap := &capture{resp: c.okBody}
		srv := cap.server(t)
		a := send(t, c.kind, map[string]string{"url": srv.URL + "/hook/SECRETTOKEN"}, problem())
		if !a.OK {
			t.Fatalf("%s: %+v", c.kind, a)
		}
		req, body := cap.last()
		if req.Header.Get("X-Unagikuma-Delivery-Id") != "d-123" || !strings.HasPrefix(req.Header.Get("User-Agent"), "unagikuma/home") {
			t.Errorf("%s headers: %v", c.kind, req.Header)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("%s: invalid JSON %s", c.kind, body)
		}
		for _, f := range c.bodyFields {
			if _, ok := m[f]; !ok {
				t.Errorf("%s: body lacks %s: %s", c.kind, f, body)
			}
		}
		if c.failBody != "" {
			cap.resp = c.failBody
			a = send(t, c.kind, map[string]string{"url": srv.URL + "/hook/SECRETTOKEN"}, problem())
			if a.OK || a.Error == "" {
				t.Errorf("%s: API error not detected: %+v", c.kind, a)
			}
		}
		cap.resp, cap.code = "", 500
		a = send(t, c.kind, map[string]string{"url": srv.URL + "/hook/SECRETTOKEN"}, problem())
		if a.OK || a.Status != 500 {
			t.Errorf("%s: HTTP 500 not detected: %+v", c.kind, a)
		}
		if strings.Contains(a.Error+a.Response, "SECRETTOKEN") {
			t.Errorf("%s: secret leaked: %+v", c.kind, a)
		}
	}
}

func TestFeishuAndDingtalkSignatures(t *testing.T) {
	cap := &capture{resp: `{"code":0}`}
	srv := cap.server(t)
	if a := send(t, "feishu", map[string]string{"url": srv.URL, "secret": "s3"}, problem()); !a.OK {
		t.Fatal(a)
	}
	_, body := cap.last()
	var raw map[string]any
	json.Unmarshal(body, &raw)
	ts, sign := raw["timestamp"].(string), raw["sign"].(string)
	h := hmac.New(sha256.New, []byte(ts+"\ns3"))
	if base64.StdEncoding.EncodeToString(h.Sum(nil)) != sign {
		t.Fatal("feishu signature")
	}
	cap2 := &capture{resp: `{"errcode":0}`}
	srv2 := cap2.server(t)
	if a := send(t, "dingtalk", map[string]string{"url": srv2.URL + "/robot/send?access_token=x", "secret": "SEC1"}, problem()); !a.OK {
		t.Fatal(a)
	}
	req, _ := cap2.last()
	q := req.URL.Query()
	h = hmac.New(sha256.New, []byte("SEC1"))
	h.Write([]byte(q.Get("timestamp") + "\nSEC1"))
	if q.Get("access_token") != "x" || base64.StdEncoding.EncodeToString(h.Sum(nil)) != q.Get("sign") {
		t.Fatalf("dingtalk signature: %v", q)
	}
}

func TestTelegram(t *testing.T) {
	cap := &capture{resp: `{"ok":true}`}
	srv := cap.server(t)
	opts := map[string]string{"token": "123:ABC", "chat": "-100", "api": srv.URL}
	if a := send(t, "telegram", opts, problem()); !a.OK {
		t.Fatal(a)
	}
	req, body := cap.last()
	if req.URL.Path != "/bot123:ABC/sendMessage" || !strings.Contains(string(body), `"chat_id":"-100"`) {
		t.Fatalf("%s %s", req.URL.Path, body)
	}
	cap.resp = `{"ok":false,"description":"chat not found"}`
	a := send(t, "telegram", opts, problem())
	if a.OK || strings.Contains(a.Error+a.Response, "123:ABC") {
		t.Fatalf("telegram failure: %+v", a)
	}
}

func TestWebhookJSONEscaping(t *testing.T) {
	cap := &capture{}
	srv := cap.server(t)
	// Default payload.
	if a := send(t, "webhook", map[string]string{"url": srv.URL}, problem()); !a.OK {
		t.Fatal(a)
	}
	_, body := cap.last()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m["monitor"] != "core-ntp" || m["delivery_id"] != "d-123" {
		t.Fatalf("default payload: %s", body)
	}
	// Template: hostile Detail must not break the JSON structure.
	tpl := `{"msg":"{{.Detail}}","raw":{{json (raw .Detail)}},"n":{{.Count}}}`
	if a := send(t, "webhook", map[string]string{"url": srv.URL, "body": tpl, "header.X-Team": "ops"}, problem()); !a.OK {
		t.Fatal(a)
	}
	req, body := cap.last()
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("template produced invalid JSON: %s", body)
	}
	if !strings.Contains(m["msg"].(string), `"quoted"`) || m["n"].(float64) != 1 || req.Header.Get("X-Team") != "ops" {
		t.Fatalf("%v", m)
	}
	// success-match.
	cap.resp = "accepted"
	if a := send(t, "webhook", map[string]string{"url": srv.URL, "success-match": "queued"}, problem()); a.OK {
		t.Fatal("success-match not enforced")
	}
}

func TestPresets(t *testing.T) {
	if len(presets) != 12 {
		t.Fatalf("%d presets", len(presets))
	}
	cap := &capture{}
	srv := cap.server(t)
	// Presets with fixed hosts are exercised through a rewriting transport.
	s := &Sender{Client: &http.Client{Transport: rewrite{srv.URL}}}
	cases := map[string]map[string]string{
		"ntfy":       {"server": srv.URL, "topic": "ops"},
		"gotify":     {"server": srv.URL, "token": "gt"},
		"bark":       {"device-key": "dk"},
		"pushover":   {"token": "t", "user-key": "u"},
		"serverchan": {"sendkey": "SCT1"},
		"pushplus":   {"token": "pp"},
		"teams":      {"url": srv.URL + "/teams"},
		"matrix":     {"homeserver": srv.URL, "token": "mt", "room": "!abc:example.org"},
		"mattermost": {"url": srv.URL + "/mm"},
		"pagerduty":  {"routing-key": "rk"},
		"opsgenie":   {"api-key": "ak", "region": "eu"},
		"googlechat": {"url": srv.URL + "/gc"},
	}
	okBodies := map[string]string{"bark": `{"code":200}`, "pushover": `{"status":1}`, "serverchan": `{"code":0}`, "pushplus": `{"code":200}`}
	for name, o := range cases {
		o["preset"] = name
		if err := ValidateChannel("webhook", o); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cap.resp = okBodies[name]
		a := s.Send(context.Background(), Channel{Name: name, Kind: "webhook", Opts: o}, problem())
		if !a.OK {
			t.Errorf("%s: %+v", name, a)
			continue
		}
		req, body := cap.last()
		if strings.Contains(req.Header.Get("Content-Type"), "json") {
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				t.Errorf("%s: invalid JSON body %s", name, body)
			}
		}
		if name == "matrix" && (req.Method != "PUT" || !strings.Contains(req.URL.EscapedPath(), "%21abc:example.org") || req.Header.Get("Authorization") != "Bearer mt") {
			t.Errorf("matrix request: %s %s %v", req.Method, req.URL.EscapedPath(), req.Header)
		}
		if name == "pagerduty" && !strings.Contains(string(body), `"dedup_key":"unagikuma-home:epoch-test:42"`) {
			t.Errorf("pagerduty dedup: %s", body)
		}
		if name == "ntfy" && (req.Header.Get("Priority") != "high" || req.Header.Get("Title") == "") {
			t.Errorf("ntfy headers %v", req.Header)
		}
	}
	m := problem()
	m.Kind = "RECOVERY"
	s.Send(context.Background(), Channel{Kind: "webhook", Opts: map[string]string{"preset": "opsgenie", "api-key": "ak"}}, m)
	if req, _ := cap.last(); !strings.HasSuffix(req.URL.Path, "/v2/alerts/unagikuma-home:epoch-test:42/close") || req.URL.Query().Get("orig_host") != "api.opsgenie.com" {
		t.Errorf("opsgenie close: %v", req.URL)
	}
	if err := ValidateChannel("webhook", map[string]string{"preset": "ntfy"}); err == nil {
		t.Error("missing topic accepted")
	}
}

// rewrite sends every request to the test server, keeping the original
// host in a query parameter for assertions.
type rewrite struct{ target string }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(r.target)
	q := req.URL.Query()
	q.Set("orig_host", req.URL.Host)
	req.URL.Scheme, req.URL.Host, req.URL.RawQuery = u.Scheme, u.Host, q.Encode()
	return http.DefaultTransport.RoundTrip(req)
}

func TestValidateChannel(t *testing.T) {
	bad := []struct {
		kind string
		opts map[string]string
	}{
		{"nope", nil},
		{"feishu", map[string]string{}},
		{"feishu", map[string]string{"url": "ftp://x"}},
		{"telegram", map[string]string{"token": "x"}},
		{"email", map[string]string{"host": "h", "from": "a@b.c", "to": "not an address"}},
		{"email", map[string]string{"host": "h", "from": "a@b.c", "to": "x@y.z", "tls": "maybe"}},
		{"webhook", map[string]string{"url": "https://x", "bogus": "1"}},
	}
	for _, b := range bad {
		if ValidateChannel(b.kind, b.opts) == nil {
			t.Errorf("accepted %s %v", b.kind, b.opts)
		}
	}
	if err := ValidateChannel("feishu", map[string]string{"url": "${secret:hook}"}); err != nil {
		t.Errorf("references must pass: %v", err)
	}
	if len(Kinds()) != 9+len(cloudKinds)+1+12 {
		t.Errorf("kinds %d", len(Kinds()))
	}
}

func TestLogChannel(t *testing.T) {
	var buf strings.Builder
	s := &Sender{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	if a := s.Send(context.Background(), Channel{Name: "l", Kind: "log"}, problem()); !a.OK {
		t.Fatal(a)
	}
	if !strings.Contains(buf.String(), "core-ntp") {
		t.Fatal(buf.String())
	}
}

// fakeSMTP accepts one message without TLS and records it.
func fakeSMTP(t *testing.T) (string, chan string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		w := func(s string) { io.WriteString(c, s+"\r\n") }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					w("250 queued")
					got <- data.String()
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				w("354 go ahead")
			case strings.HasPrefix(cmd, "QUIT"):
				w("221 bye")
				return
			case strings.HasPrefix(cmd, "RCPT") && strings.Contains(cmd, "GONE@"):
				w("550 5.1.1 <gone@example.com>: Recipient address rejected: User unknown")
			case strings.HasPrefix(cmd, "RCPT") && strings.Contains(cmd, "FULL@"):
				w("452 4.2.2 mailbox full")
			default:
				w("250 ok")
			}
		}
	}()
	return l.Addr().String(), got
}

func TestEmail(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	opts := map[string]string{"host": host, "port": port, "tls": "none", "from": "unagikuma <mon@example.com>", "to": "ops@example.com, oncall@example.com"}
	if a := send(t, "email", opts, problem()); !a.OK {
		t.Fatalf("%+v", a)
	}
	msg := <-got
	for _, want := range []string{"Subject: =?utf-8?q?", "X-Unagikuma-Delivery-Id: d-123", "Content-Transfer-Encoding: base64", "To: <ops@example.com>, <oncall@example.com>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	// Credentials are never sent without TLS.
	opts["username"], opts["password"] = "u", "p"
	addr2, _ := fakeSMTP(t)
	host2, port2, _ := net.SplitHostPort(addr2)
	opts["host"], opts["port"] = host2, port2
	if a := send(t, "email", opts, problem()); a.OK || !strings.Contains(a.Error, "TLS") {
		t.Fatalf("%+v", a)
	}
}

// A recipient that does not exist is a bounce: the others still get the
// message. A temporary refusal is not a bounce.
func TestEmailBounce(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	opts := map[string]string{"host": host, "port": port, "tls": "none", "from": "mon@example.com", "to": "ops@example.com, gone@example.com, full@example.com"}
	a := send(t, "email", opts, problem())
	if !a.OK || len(a.Bounces) != 1 || a.Bounces[0].Address != "gone@example.com" {
		t.Fatalf("%+v", a)
	}
	<-got
	addr2, _ := fakeSMTP(t)
	host2, port2, _ := net.SplitHostPort(addr2)
	opts = map[string]string{"host": host2, "port": port2, "tls": "none", "from": "mon@example.com", "to": "gone@example.com"}
	if a := send(t, "email", opts, problem()); a.OK || len(a.Bounces) != 1 {
		t.Fatalf("only an unknown recipient: %+v", a)
	}
}

func TestRedaction(t *testing.T) {
	if RedactURL("https://open.feishu.cn/open-apis/bot/v2/hook/abc") != "https://open.feishu.cn/…" {
		t.Fatal(RedactURL("https://open.feishu.cn/open-apis/bot/v2/hook/abc"))
	}
	if got := scrub(`Post "https://api.telegram.org/bot123:XYZ/sendMessage": dial tcp`, nil); strings.Contains(got, "XYZ") {
		t.Fatal(got)
	}
	if Redact("token=supersecret", []string{"supersecret"}) != "token=***" {
		t.Fatal("redact")
	}
}

// A detail that only repeats the reason is not written twice.
func TestRenderNoRepeatedReason(t *testing.T) {
	m := &Message{Kind: "PROBLEM", Monitor: "web", Title: "web", Status: "CRITICAL", Reason: "timeout", Detail: "no response within the timeout", Lang: "en"}
	_, body := Render(m)
	if n := strings.Count(strings.ToLower(body), "no response within the timeout"); n != 1 {
		t.Fatalf("reason written %d times:\n%s", n, body)
	}
}
