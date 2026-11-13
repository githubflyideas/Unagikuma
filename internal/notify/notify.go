package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Sender delivers messages. It is safe for concurrent use.
type Sender struct {
	// Client is used for every HTTP channel; nil uses a default client
	// that never follows the environment's proxy settings.
	Client *http.Client
	// Log receives messages of the "log" channel kind.
	Log *slog.Logger
	// Instance names this unagikuma in the User-Agent.
	Instance string
}

func (s *Sender) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true}, CheckRedirect: noRedirect}
}

// Webhooks must not be redirected: a redirect could carry the message (and
// signed headers) to an unexpected host.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Send delivers m through ch. It never panics; failures are reported in
// the Attempt with a short, credential-free error.
func (s *Sender) Send(ctx context.Context, ch Channel, m *Message) (a Attempt) {
	start := time.Now()
	a.Channel = ch.Name
	defer func() {
		if v := recover(); v != nil {
			a.OK, a.Error = false, fmt.Sprintf("internal error: %v", v)
		}
		a.Latency = time.Since(start)
		a.Error = scrub(a.Error, secretsOf(ch))
		a.Response = scrub(a.Response, secretsOf(ch))
	}()
	if err := ValidateChannel(ch.Kind, ch.Opts); err != nil {
		a.Error = err.Error()
		return a
	}
	switch ch.Kind {
	case "log":
		title, body := Render(m)
		lg := s.Log
		if lg == nil {
			lg = slog.Default()
		}
		lg.Info("notification", "channel", ch.Name, "kind", m.Kind, "title", title, "body", body, "delivery_id", m.DeliveryID)
		a.OK = true
		return a
	case "email":
		var err error
		a.Bounces, err = sendEmail(ctx, ch.Opts, m)
		a.OK = err == nil
		if err != nil {
			classify(&a, err)
		}
		return a
	}
	if isCustomKind(ch.Kind) {
		var err error
		a.Bounces, err = s.sendCustom(ctx, ch, m)
		a.OK = err == nil
		if err != nil {
			classify(&a, err)
		}
		return a
	}
	if isCloudKind(ch.Kind) {
		var err error
		a.Bounces, err = s.sendCloud(ctx, ch, m)
		a.OK = err == nil
		if err != nil {
			classify(&a, err)
		}
		return a
	}
	req, check, err := s.build(ctx, ch, m)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	return s.do(req, check, a)
}

// secretsOf lists option values that must never appear in logs.
func secretsOf(ch Channel) []string {
	var out []string
	for k, v := range ch.Opts {
		if isSecretField(ch.Kind, k) && v != "" {
			out = append(out, v)
			if u, err := url.Parse(v); err == nil && u.Path != "" {
				out = append(out, strings.TrimPrefix(u.Path, "/"))
			}
		}
	}
	return out
}

// KindInfo describes a channel kind for the UI picker.
type KindInfo struct {
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	Preset bool    `json:"preset,omitempty"`
	Group  string  `json:"group,omitempty"`
	Fields []Field `json:"fields"`
}

// Field is one form field of a channel kind. Labels are translated by the
// UI from "channel.field.<key>".
type Field struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"` // text | secret | url | number | select | email-list | textarea
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
	// Examples are ready request bodies the form offers for this field.
	Examples []Example `json:"examples,omitempty"`
}

// Example is a ready request body for a common receiving API: the form
// fills it in, and only the text needs changing.
type Example struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// webhookExamples are the request bodies of the APIs a custom webhook is
// most often pointed at. {{.Text}} is the whole message (title and body).
var webhookExamples = []Example{
	{"wecom-app", `{"touser":"@all","msgtype":"text","agentid":1000002,"text":{"content":"{{.Text}}"}}`},
	{"wecom-bot", `{"msgtype":"text","text":{"content":"{{.Text}}"}}`},
	{"dingtalk", `{"msgtype":"text","text":{"content":"{{.Text}}"},"at":{"isAtAll":false}}`},
	{"feishu", `{"msg_type":"text","content":{"text":"{{.Text}}"}}`},
	{"slack", `{"text":"{{.Text}}"}`},
	{"discord", `{"content":"{{.Text}}"}`},
	{"telegram", `{"chat_id":"123456789","text":"{{.Text}}"}`},
	{"generic", `{"title":"{{.Title}}","kind":"{{.Kind}}","status":"{{.StatusText}}","monitor":"{{.Name}}","target":"{{.Target}}","reason":"{{.ReasonText}}","detail":"{{.Detail}}","started":"{{.Started}}","duration":"{{.DurationText}}","url":"{{.DetailURL}}"}`},
	{"kuma", `{"touser":"@all","msgtype":"text","text":{"content":"{% if heartbeatJSON.status == 1 %}✅ UP{% else %}🔴 DOWN{% endif %} {{ monitorJSON.name }}\n{{ monitorJSON.hostname }}\n{{ heartbeatJSON.msg }}\n{{ heartbeatJSON.localDateTime }}"}}`},
}

// tplField is the message template of a channel that sends text; atField
// names who a chat robot mentions.
var (
	tplField = Field{Key: "template", Type: "textarea", Placeholder: "{{.Icon}} {{.KindText}}: {{.Name}} {{.StatusText}}\n{{.ReasonText}} — {{.Detail}}\n{{.DetailURL}}"}
	// prefixField marks each text message with the site that sent it.
	prefixField = Field{Key: "prefix", Type: "text", Placeholder: "[Beijing unagi-host1]"}
	atField     = Field{Key: "at", Type: "text", Placeholder: "all | 13800000000,13900000000"}
)

var nativeKinds = []KindInfo{
	{Kind: "feishu", Label: "Feishu / Lark", Fields: []Field{{Key: "url", Type: "secret", Required: true, Placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/…"}, {Key: "secret", Type: "secret"}, {Key: "at", Type: "text", Placeholder: "all | ou_xxxxxxxx"}, tplField}},
	{Kind: "dingtalk", Label: "DingTalk", Fields: []Field{{Key: "url", Type: "secret", Required: true, Placeholder: "https://oapi.dingtalk.com/robot/send?access_token=…"}, {Key: "secret", Type: "secret"}, atField, tplField}},
	{Kind: "wecom", Label: "WeCom", Fields: []Field{{Key: "url", Type: "secret", Required: true, Placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…"}, atField, tplField}},
	{Kind: "telegram", Label: "Telegram", Fields: []Field{{Key: "token", Type: "secret", Required: true}, {Key: "chat", Type: "text", Required: true}, {Key: "api", Type: "url", Default: "https://api.telegram.org"}, tplField}},
	{Kind: "slack", Label: "Slack", Fields: []Field{{Key: "url", Type: "secret", Required: true, Placeholder: "https://hooks.slack.com/services/…"}, tplField}},
	{Kind: "discord", Label: "Discord", Fields: []Field{{Key: "url", Type: "secret", Required: true, Placeholder: "https://discord.com/api/webhooks/…"}, tplField}},
	{Kind: "email", Label: "Email", Fields: []Field{{Key: "via", Type: "select", Options: []string{"smtp", "sendmail"}, Default: "smtp"}, {Key: "host", Type: "text"}, {Key: "port", Type: "number", Default: "587"}, {Key: "username", Type: "text"}, {Key: "password", Type: "secret"}, {Key: "tls", Type: "select", Options: []string{"starttls", "tls", "none"}}, {Key: "program", Type: "text", Placeholder: "/usr/sbin/sendmail"}, {Key: "from", Type: "text"}, {Key: "to", Type: "email-list", Required: true}, tplField}},
	{Kind: "webhook", Label: "Custom webhook", Fields: []Field{{Key: "url", Type: "url", Required: true}, {Key: "method", Type: "select", Options: []string{"POST", "PUT", "GET"}, Default: "POST"}, {Key: "content-type", Type: "select", Options: []string{"json", "form", "text"}, Default: "json"}, {Key: "body", Type: "textarea", Examples: webhookExamples}, {Key: "success-match", Type: "text"}}},
	{Kind: "log", Label: "Log only", Fields: nil},
}

func init() { nativeKinds = append(nativeKinds, cloudKinds...) }

// Kinds lists native channel kinds and presets.
func Kinds() []KindInfo {
	out := append([]KindInfo(nil), nativeKinds...)
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := presets[n]
		out = append(out, KindInfo{Kind: "webhook:" + n, Label: p.Label, Preset: true, Group: p.Group, Fields: append(append([]Field(nil), p.Fields...), tplField)})
	}
	// A contact group is resolved by the engine, not sent here.
	out = append(out, KindInfo{Kind: "group", Label: "Contact group", Fields: []Field{{Key: "members", Type: "text", Required: true, Placeholder: "ops-feishu,ops-sms"}}})
	for i := range out {
		if out[i].Fields == nil {
			out[i].Fields = []Field{} // JSON [] rather than null
		}
	}
	return out
}

func kindInfo(kind string) (KindInfo, bool) {
	for _, k := range nativeKinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return KindInfo{}, false
}

func isSecretField(kind, key string) bool {
	switch key {
	case "url":
		return kind != "webhook"
	case "token", "secret", "password", "key", "sendkey", "routing-key", "api-key", "device-key", "user-key", "access-key-secret", "secret-key", "auth-token":
		return true
	}
	return false
}

// ValidateChannel checks a channel's options.
func ValidateChannel(kind string, opts map[string]string) error {
	if kind == "webhook" && opts["preset"] != "" {
		p, ok := presets[opts["preset"]]
		if !ok {
			return fmt.Errorf("unknown preset %q", opts["preset"])
		}
		return validateFields(p.Fields, opts, map[string]bool{"preset": true, "template": true, "success-match": true})
	}
	info, ok := kindInfo(kind)
	if !ok {
		return fmt.Errorf("unknown channel kind %q", kind)
	}
	// template= on a kind that has no text of its own (an SMS template of
	// the provider, a log line) was always accepted and is still: it does
	// nothing there.
	extra := map[string]bool{"template": true}
	if kind == "webhook" || isCustomKind(kind) {
		for k := range opts {
			if strings.HasPrefix(k, "header.") {
				extra[k] = true
			}
		}
	}
	if err := validateFields(info.Fields, opts, extra); err != nil {
		return err
	}
	if kind == "email" {
		return validateEmail(opts)
	}
	return nil
}

// TemplateProblems tries the templates an operator wrote for a channel on
// a made-up message: one that does not parse, or uses a name that does
// not exist, is reported. A configuration file with such a template still
// loads (the built-in text is sent, a missing name is empty); the console
// refuses to save one.
func TemplateProblems(kind string, opts map[string]string) []string {
	var out []string
	keys := []string{"template"}
	if (kind == "webhook" && opts["preset"] == "") || isCustomKind(kind) {
		if isCustomKind(kind) {
			o := map[string]string{"Phone": "13800000000"}
			for k, v := range opts {
				o[k] = v
			}
			opts = o
		}
		keys = append(keys, "body")
		if strings.Contains(opts["url"], "{{") {
			keys = append(keys, "url")
		}
	}
	if isCloudKind(kind) && !strings.HasPrefix(kind, "twilio-") {
		keys = append(keys, "params")
	}
	for _, k := range keys {
		if err := checkTemplate(k, opts[k], opts); err != nil {
			out = append(out, err.Error())
		}
	}
	return out
}

// smtpOnly are the e-mail options that mean nothing to the local
// sendmail command, and programOnly the reverse.
var smtpOnly, programOnly = []string{"host", "port", "username", "password", "tls"}, []string{"program"}

// validateEmail checks the options of one way of sending: an SMTP server
// needs host and from; the local sendmail command needs neither (the mail
// system knows where to relay and whom to send as).
func validateEmail(o map[string]string) error {
	if strings.EqualFold(o["via"], "sendmail") {
		for _, k := range smtpOnly {
			if o[k] != "" {
				return fmt.Errorf("%s is for via=smtp; the sendmail command uses the mail system's own settings", k)
			}
		}
		return checkSendmailProgram(o["program"])
	}
	for _, k := range programOnly {
		if o[k] != "" {
			return fmt.Errorf("%s is for via=sendmail", k)
		}
	}
	for _, k := range []string{"host", "from"} {
		if o[k] == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}

func validateFields(fields []Field, opts map[string]string, extra map[string]bool) error {
	known := map[string]Field{}
	for _, f := range fields {
		known[f.Key] = f
		v, has := opts[f.Key]
		if f.Required && (!has || v == "") {
			return fmt.Errorf("%s is required", f.Key)
		}
	}
	for k, v := range opts {
		f, ok := known[k]
		if !ok {
			if extra[k] {
				continue
			}
			return fmt.Errorf("unknown option %s", k)
		}
		if v == "" || strings.Contains(v, "${") {
			continue // references are resolved later
		}
		switch f.Type {
		case "url":
			if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("%s must be an http(s) URL", k)
			}
		case "secret":
			if (k == "url") && !strings.HasPrefix(v, "http") {
				return fmt.Errorf("%s must be an http(s) URL", k)
			}
		case "select":
			found := false
			for _, o := range f.Options {
				found = found || strings.EqualFold(o, v)
			}
			if !found {
				return fmt.Errorf("%s must be one of %s", k, strings.Join(f.Options, ", "))
			}
		case "email-list":
			// @handles name people of the contact directory; they are
			// replaced by their addresses before sending.
			if rest := withoutHandles(v); rest != "" {
				if _, err := mail.ParseAddressList(rest); err != nil {
					return fmt.Errorf("%s: invalid address list", k)
				}
			}
		case "number":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("%s must be a port number", k)
			}
		}
	}
	return nil
}

var handleRe = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]{0,62}$`)

var handleItemRe = regexp.MustCompile(`(^|,)\s*@`)

// withoutHandles drops the @handles from a recipient list (a list without
// any is returned as is, display names with commas included).
func withoutHandles(v string) string {
	if !handleItemRe.MatchString(v) {
		return v
	}
	var keep []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" && !handleRe.MatchString(strings.ToLower(p)) {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, ", ")
}
