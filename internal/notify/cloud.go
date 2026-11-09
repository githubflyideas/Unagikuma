package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Aliyun's RPC signature is defined with HMAC-SHA1
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// SMS and voice channels of Aliyun, Tencent Cloud and Twilio. Chinese
// providers only send approved templates: the channel names the template
// and fills its variables from params=, a template over the message whose
// result is split on "|" (default: the monitor name and its status).

var cloudKinds = []KindInfo{
	{Kind: "aliyun-sms", Label: "Aliyun SMS", Fields: []Field{
		{Key: "access-key-id", Type: "text", Required: true}, {Key: "access-key-secret", Type: "secret", Required: true},
		{Key: "sign-name", Type: "text", Required: true}, {Key: "template-code", Type: "text", Required: true, Placeholder: "SMS_123456789"},
		{Key: "phones", Type: "text", Required: true, Placeholder: "13800000000,13900000000"},
		{Key: "params", Type: "text", Default: defaultSMSParams, Placeholder: "name|status"},
		{Key: "endpoint", Type: "url", Default: "https://dysmsapi.aliyuncs.com"}}},
	{Kind: "aliyun-voice", Label: "Aliyun voice call", Fields: []Field{
		{Key: "access-key-id", Type: "text", Required: true}, {Key: "access-key-secret", Type: "secret", Required: true},
		{Key: "tts-code", Type: "text", Required: true, Placeholder: "TTS_123456789"}, {Key: "phones", Type: "text", Required: true},
		{Key: "show-number", Type: "text"}, {Key: "params", Type: "text", Default: defaultSMSParams},
		{Key: "endpoint", Type: "url", Default: "https://dyvmsapi.aliyuncs.com"}}},
	{Kind: "tencent-sms", Label: "Tencent Cloud SMS", Fields: []Field{
		{Key: "secret-id", Type: "text", Required: true}, {Key: "secret-key", Type: "secret", Required: true},
		{Key: "app-id", Type: "text", Required: true}, {Key: "sign-name", Type: "text", Required: true},
		{Key: "template-id", Type: "text", Required: true}, {Key: "phones", Type: "text", Required: true, Placeholder: "+8613800000000"},
		{Key: "params", Type: "text", Default: defaultSMSParams}, {Key: "region", Type: "text", Default: "ap-guangzhou"},
		{Key: "endpoint", Type: "url", Default: "https://sms.tencentcloudapi.com"}}},
	{Kind: "tencent-voice", Label: "Tencent Cloud voice call", Fields: []Field{
		{Key: "secret-id", Type: "text", Required: true}, {Key: "secret-key", Type: "secret", Required: true},
		{Key: "app-id", Type: "text", Required: true}, {Key: "template-id", Type: "text", Required: true},
		{Key: "phones", Type: "text", Required: true}, {Key: "params", Type: "text", Default: defaultSMSParams},
		{Key: "region", Type: "text", Default: "ap-guangzhou"}, {Key: "endpoint", Type: "url", Default: "https://vms.tencentcloudapi.com"}}},
	{Kind: "twilio-sms", Label: "Twilio SMS", Fields: []Field{
		{Key: "account-sid", Type: "text", Required: true}, {Key: "auth-token", Type: "secret", Required: true},
		{Key: "from", Type: "text", Required: true}, {Key: "phones", Type: "text", Required: true, Placeholder: "+15550100"},
		{Key: "api", Type: "url", Default: "https://api.twilio.com"}}},
	{Kind: "twilio-voice", Label: "Twilio voice call", Fields: []Field{
		{Key: "account-sid", Type: "text", Required: true}, {Key: "auth-token", Type: "secret", Required: true},
		{Key: "from", Type: "text", Required: true}, {Key: "phones", Type: "text", Required: true},
		{Key: "api", Type: "url", Default: "https://api.twilio.com"}}},
}

// Template variables are short on Chinese SMS (often 20-35 characters).
const defaultSMSParams = "{{trunc 20 .Name}}|{{.StatusText}}"

func isCloudKind(kind string) bool {
	return strings.HasPrefix(kind, "aliyun-") || strings.HasPrefix(kind, "tencent-") || strings.HasPrefix(kind, "twilio-")
}

func optOr(o map[string]string, k, def string) string {
	if v := o[k]; v != "" {
		return v
	}
	return def
}

func phonesOf(o map[string]string) []string {
	var out []string
	for _, p := range strings.Split(o["phones"], ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// smsParams renders params= into the template's variable values.
func smsParams(o map[string]string, m *Message) ([]string, error) {
	t, err := template.New("p").Funcs(funcs).Option("missingkey=zero").Parse(optOr(o, "params", defaultSMSParams))
	if err != nil {
		return nil, fmt.Errorf("params: %v", err)
	}
	d := templateData(m, nil, false)
	d["StatusText"] = statusText(m)
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return nil, fmt.Errorf("params: %v", err)
	}
	return strings.Split(b.String(), "|"), nil
}

// statusText is the state in the message language: "故障", "恢复", ...
func statusText(m *Message) string {
	switch m.Kind {
	case "RECOVERY":
		return i18n.T(m.Lang, "msg.kind.RECOVERY")
	case "PROBLEM", "REPEAT", "REOPEN", "ESCALATE":
		if m.Status != "" {
			return i18n.T(m.Lang, "status."+m.Status)
		}
	}
	return i18n.T(m.Lang, "msg.kind."+m.Kind)
}

// sendCloud delivers to every phone, one number per request so a wrong
// number neither blocks the others nor hides which one it was; it
// succeeds when at least one number got it. Numbers the provider calls
// invalid come back as bounces.
func (s *Sender) sendCloud(ctx context.Context, ch Channel, m *Message) ([]Bounce, error) {
	o := ch.Opts
	phones := phonesOf(o)
	if len(phones) == 0 {
		return nil, errors.New("no phone numbers")
	}
	var errs []string
	var bounces []Bounce
	sent := 0
	send := func(err error) {
		var be *bounceError
		switch {
		case err == nil:
			sent++
		case errors.As(err, &be):
			bounces = append(bounces, be.Bounce)
			errs = append(errs, err.Error())
		default:
			errs = append(errs, err.Error())
		}
	}
	var vals []string
	if !strings.HasPrefix(ch.Kind, "twilio-") {
		var err error
		if vals, err = smsParams(o, m); err != nil {
			return nil, err
		}
	}
	for _, p := range phones {
		switch ch.Kind {
		case "aliyun-sms":
			send(s.aliyun(ctx, o, p, "https://dysmsapi.aliyuncs.com", map[string]string{
				"Action": "SendSms", "Version": "2017-05-25", "RegionId": "cn-hangzhou", "PhoneNumbers": p,
				"SignName": o["sign-name"], "TemplateCode": o["template-code"], "TemplateParam": namedParams(vals),
			}))
		case "aliyun-voice":
			q := map[string]string{"Action": "SingleCallByTts", "Version": "2017-05-25", "RegionId": "cn-hangzhou",
				"CalledNumber": p, "TtsCode": o["tts-code"], "TtsParam": namedParams(vals)}
			if n := o["show-number"]; n != "" {
				q["CalledShowNumber"] = n
			}
			send(s.aliyun(ctx, o, p, "https://dyvmsapi.aliyuncs.com", q))
		case "tencent-sms":
			send(s.tencent(ctx, o, p, "sms", "https://sms.tencentcloudapi.com", "SendSms", "2021-01-11", map[string]any{
				"PhoneNumberSet": []string{p}, "SmsSdkAppId": o["app-id"], "SignName": o["sign-name"],
				"TemplateId": o["template-id"], "TemplateParamSet": vals,
			}))
		case "tencent-voice":
			send(s.tencent(ctx, o, p, "vms", "https://vms.tencentcloudapi.com", "SendTtsVoice", "2020-09-02", map[string]any{
				"TemplateId": o["template-id"], "TemplateParamSet": vals, "CalledNumber": p, "VoiceSdkAppid": o["app-id"], "PlayTimes": 2,
			}))
		case "twilio-sms", "twilio-voice":
			title, _ := Render(m)
			f := url.Values{"To": {p}, "From": {o["from"]}}
			res := "Messages"
			if ch.Kind == "twilio-voice" {
				res = "Calls"
				say := html.EscapeString(title) // TwiML is XML
				f.Set("Twiml", "<Response><Say>"+say+"</Say><Pause length=\"1\"/><Say>"+say+"</Say></Response>")
			} else {
				f.Set("Body", title)
			}
			send(s.twilio(ctx, o, p, res, f))
		default:
			return nil, fmt.Errorf("unknown kind %s", ch.Kind)
		}
	}
	if sent == 0 {
		return bounces, errors.New(strings.Join(errs, "; "))
	}
	return bounces, nil
}

// Provider error codes that mean the number itself is wrong (it will fail
// the same way every time), as opposed to quota, signature or template
// problems.
var (
	aliyunBadNumber = map[string]bool{"isv.MOBILE_NUMBER_ILLEGAL": true, "isv.CALLED_NUMBER_ILLEGAL": true, "isv.INVALID_PARAMETERS_NUMBER": true}
	twilioBadNumber = map[int]bool{21211: true, 21214: true, 21217: true, 21401: true, 21610: true, 21612: true, 21614: true}
)

func tencentBadNumber(code string) bool {
	return strings.Contains(code, "PhoneNumber") || strings.Contains(code, "InvalidNumber") || strings.Contains(code, "CalledNumber")
}

// namedParams turns the values into Aliyun's TemplateParam JSON: the
// first two are "name" and "status", any more are "p3", "p4", ...
func namedParams(vals []string) string {
	m := map[string]string{}
	for i, v := range vals {
		switch i {
		case 0:
			m["name"] = v
		case 1:
			m["status"] = v
		default:
			m["p"+strconv.Itoa(i+1)] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func percentEncode(s string) string {
	return strings.NewReplacer("+", "%20", "*", "%2A", "%7E", "~").Replace(url.QueryEscape(s))
}

// aliyun signs an RPC request (signature version 1.0) and checks Code=OK.
func (s *Sender) aliyun(ctx context.Context, o map[string]string, phone, def string, q map[string]string) error {
	q["AccessKeyId"], q["Format"], q["SignatureMethod"], q["SignatureVersion"] = o["access-key-id"], "JSON", "HMAC-SHA1", "1.0"
	q["SignatureNonce"] = strconv.FormatInt(time.Now().UnixNano(), 36)
	q["Timestamp"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, percentEncode(k)+"="+percentEncode(q[k]))
	}
	canon := strings.Join(parts, "&")
	mac := hmac.New(sha1.New, []byte(o["access-key-secret"]+"&"))
	mac.Write([]byte("GET&%2F&" + percentEncode(canon)))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	u := strings.TrimRight(optOr(o, "endpoint", def), "/") + "/?Signature=" + percentEncode(sig) + "&" + canon
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	var out struct{ Code, Message string }
	if err := s.doJSON(req, &out); err != nil {
		return err
	}
	if out.Code != "OK" {
		if aliyunBadNumber[out.Code] {
			return &bounceError{Bounce{Address: phone, Reason: "aliyun: " + out.Code + " " + out.Message}}
		}
		return fmt.Errorf("aliyun: %s %s", out.Code, out.Message)
	}
	return nil
}

// tencent signs a TC3-HMAC-SHA256 request.
func (s *Sender) tencent(ctx context.Context, o map[string]string, phone, service, def, action, version string, body map[string]any) error {
	payload, _ := json.Marshal(body)
	endpoint := strings.TrimRight(optOr(o, "endpoint", def), "/")
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	ts := time.Now().Unix()
	date := time.Unix(ts, 0).UTC().Format("2006-01-02")
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	canon := "POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:" + host + "\n\ncontent-type;host\n" + sum(payload)
	scope := date + "/" + service + "/tc3_request"
	toSign := "TC3-HMAC-SHA256\n" + strconv.FormatInt(ts, 10) + "\n" + scope + "\n" + sum([]byte(canon))
	h := func(key []byte, msg string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(msg))
		return m.Sum(nil)
	}
	key := h(h(h([]byte("TC3"+o["secret-key"]), date), service), "tc3_request")
	sig := hex.EncodeToString(h(key, toSign))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "TC3-HMAC-SHA256 Credential="+o["secret-id"]+"/"+scope+", SignedHeaders=content-type;host, Signature="+sig)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-TC-Region", optOr(o, "region", "ap-guangzhou"))
	var out struct {
		Response struct {
			Error *struct{ Code, Message string }
			// SMS reports each number (one per request here).
			SendStatusSet []struct{ Code, Message string }
		}
	}
	if err := s.doJSON(req, &out); err != nil {
		return err
	}
	code, msg := "", ""
	if e := out.Response.Error; e != nil {
		code, msg = e.Code, e.Message
	} else if set := out.Response.SendStatusSet; len(set) > 0 && !strings.EqualFold(set[0].Code, "Ok") {
		code, msg = set[0].Code, set[0].Message
	}
	switch {
	case code == "":
		return nil
	case tencentBadNumber(code):
		return &bounceError{Bounce{Address: phone, Reason: "tencent cloud: " + code + " " + msg}}
	}
	return fmt.Errorf("tencent cloud: %s %s", code, msg)
}

func (s *Sender) twilio(ctx context.Context, o map[string]string, phone, resource string, f url.Values) error {
	u := strings.TrimRight(optOr(o, "api", "https://api.twilio.com"), "/") + "/2010-04-01/Accounts/" + url.PathEscape(o["account-sid"]) + "/" + resource + ".json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(f.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(o["account-sid"], o["auth-token"])
	var out struct {
		Sid     string `json:"sid"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := s.doJSON(req, &out); err != nil {
		if twilioBadNumber[out.Code] {
			return &bounceError{Bounce{Address: phone, Reason: fmt.Sprintf("twilio %d: %s", out.Code, out.Message)}}
		}
		return err
	}
	if out.Sid == "" {
		return fmt.Errorf("twilio: %s", out.Message)
	}
	return nil
}

// doJSON performs req and decodes a JSON reply; non-2xx replies are
// errors carrying the provider's message.
func (s *Sender) doJSON(req *http.Request, out any) error {
	req.Header.Set("User-Agent", "unagikuma")
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, sanitize(string(b), 200))
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, sanitize(string(b), 200))
	}
	return nil
}
