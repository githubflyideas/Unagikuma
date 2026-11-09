package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake providers check the signature with the same secret, so a wrong
// canonical form fails the test.

func TestAliyunSMSAndVoice(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		params := map[string]string{}
		var keys []string
		for k := range q {
			if k != "Signature" {
				keys = append(keys, k)
				params[k] = q.Get(k)
			}
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, percentEncode(k)+"="+percentEncode(q.Get(k)))
		}
		mac := hmac.New(sha1.New, []byte("s3cret&"))
		mac.Write([]byte("GET&%2F&" + percentEncode(strings.Join(parts, "&"))))
		if base64.StdEncoding.EncodeToString(mac.Sum(nil)) != q.Get("Signature") {
			w.Write([]byte(`{"Code":"SignatureDoesNotMatch","Message":"bad signature"}`))
			return
		}
		mu.Lock()
		got = append(got, params)
		mu.Unlock()
		w.Write([]byte(`{"Code":"OK","Message":"OK"}`))
	}))
	defer srv.Close()
	s := &Sender{}
	m := problem()
	m.Lang = "zh"
	sms := Channel{Name: "sms", Kind: "aliyun-sms", Opts: map[string]string{
		"access-key-id": "AK", "access-key-secret": "s3cret", "sign-name": "运维", "template-code": "SMS_1",
		"phones": "13800000000, 13900000000", "endpoint": srv.URL}}
	if a := s.Send(context.Background(), sms, m); !a.OK {
		t.Fatalf("sms: %+v", a)
	}
	// One request per number, so a wrong number is told apart.
	if len(got) != 2 || got[0]["PhoneNumbers"] != "13800000000" || got[1]["PhoneNumbers"] != "13900000000" || got[0]["Action"] != "SendSms" {
		t.Fatalf("sms request: %v", got)
	}
	var tp map[string]string
	json.Unmarshal([]byte(got[0]["TemplateParam"]), &tp)
	if tp["name"] == "" || tp["status"] != "故障" {
		t.Fatalf("template params: %v", tp)
	}
	voice := Channel{Name: "call", Kind: "aliyun-voice", Opts: map[string]string{
		"access-key-id": "AK", "access-key-secret": "s3cret", "tts-code": "TTS_1", "phones": "13800000000,13900000000", "endpoint": srv.URL}}
	if a := s.Send(context.Background(), voice, m); !a.OK {
		t.Fatalf("voice: %+v", a)
	}
	if len(got) != 4 || got[2]["Action"] != "SingleCallByTts" || got[3]["CalledNumber"] != "13900000000" {
		t.Fatalf("one call per number: %v", got)
	}
	bad := sms
	bad.Opts = map[string]string{}
	for k, v := range sms.Opts {
		bad.Opts[k] = v
	}
	bad.Opts["access-key-secret"] = "wrong"
	if a := s.Send(context.Background(), bad, m); a.OK || !strings.Contains(a.Error, "SignatureDoesNotMatch") || strings.Contains(a.Error, "wrong") {
		t.Fatalf("bad key: %+v", a)
	}
}

func TestTencentSMS(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		ts := r.Header.Get("X-TC-Timestamp")
		n, _ := strconv.ParseInt(ts, 10, 64)
		date := time.Unix(n, 0).UTC().Format("2006-01-02")
		sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
		canon := "POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:" + r.Host + "\n\ncontent-type;host\n" + sum(payload)
		scope := date + "/sms/tc3_request"
		toSign := "TC3-HMAC-SHA256\n" + ts + "\n" + scope + "\n" + sum([]byte(canon))
		h := func(k []byte, m string) []byte { x := hmac.New(sha256.New, k); x.Write([]byte(m)); return x.Sum(nil) }
		key := h(h(h([]byte("TC3key"), date), "sms"), "tc3_request")
		want := "TC3-HMAC-SHA256 Credential=SID/" + scope + ", SignedHeaders=content-type;host, Signature=" + hex.EncodeToString(h(key, toSign))
		if r.Header.Get("Authorization") != want || r.Header.Get("X-TC-Action") != "SendSms" {
			w.Write([]byte(`{"Response":{"Error":{"Code":"AuthFailure.SignatureFailure","Message":"bad"}}}`))
			return
		}
		json.Unmarshal(payload, &body)
		w.Write([]byte(`{"Response":{"SendStatusSet":[{"Code":"Ok"},{"Code":"LimitExceeded.PhoneNumberDailyLimit"}]}}`))
	}))
	defer srv.Close()
	ch := Channel{Name: "tx", Kind: "tencent-sms", Opts: map[string]string{
		"secret-id": "SID", "secret-key": "key", "app-id": "1400", "sign-name": "运维", "template-id": "123",
		"phones": "+8613800000000,+8613900000000", "endpoint": srv.URL, "params": "{{.Monitor}}|{{.StatusText}}|x"}}
	m := problem()
	if a := (&Sender{}).Send(context.Background(), ch, m); !a.OK {
		t.Fatalf("%+v", a)
	}
	if set, _ := body["TemplateParamSet"].([]any); len(set) != 3 || set[0] != m.Monitor || set[2] != "x" {
		t.Fatalf("params: %v", body)
	}
}

func TestTwilio(t *testing.T) {
	var forms []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		if u != "AC1" || p != "tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Authenticate"}`))
			return
		}
		r.ParseForm()
		forms = append(forms, r.URL.Path+" "+r.Form.Get("To")+" "+r.Form.Get("Twiml")+r.Form.Get("Body"))
		w.WriteHeader(201)
		w.Write([]byte(`{"sid":"SM1"}`))
	}))
	defer srv.Close()
	m := problem()
	m.Title = "a & <b>"
	for _, kind := range []string{"twilio-sms", "twilio-voice"} {
		ch := Channel{Name: kind, Kind: kind, Opts: map[string]string{"account-sid": "AC1", "auth-token": "tok", "from": "+1555", "phones": "+1666", "api": srv.URL}}
		if a := (&Sender{}).Send(context.Background(), ch, m); !a.OK {
			t.Fatalf("%s: %+v", kind, a)
		}
	}
	if len(forms) != 2 || !strings.Contains(forms[0], "/Messages.json +1666") || !strings.Contains(forms[1], "/Calls.json") || !strings.Contains(forms[1], "&amp; &lt;b&gt;") {
		t.Fatalf("%q", forms)
	}
	ch := Channel{Name: "x", Kind: "twilio-sms", Opts: map[string]string{"account-sid": "AC1", "auth-token": "nope", "from": "+1555", "phones": "+1666", "api": srv.URL}}
	if a := (&Sender{}).Send(context.Background(), ch, m); a.OK || strings.Contains(a.Error, "nope") {
		t.Fatalf("bad token: %+v", a)
	}
}

// A number the provider calls invalid is a bounce; the other numbers of
// the channel still get the message, and other errors are not bounces.
func TestSMSBadNumberIsABounce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("PhoneNumbers") {
		case "10000000000":
			w.Write([]byte(`{"Code":"isv.MOBILE_NUMBER_ILLEGAL","Message":"invalid number"}`))
		case "13700000000":
			w.Write([]byte(`{"Code":"isv.BUSINESS_LIMIT_CONTROL","Message":"too many"}`))
		default:
			w.Write([]byte(`{"Code":"OK","Message":"OK"}`))
		}
	}))
	defer srv.Close()
	s := &Sender{}
	ch := Channel{Name: "sms", Kind: "aliyun-sms", Opts: map[string]string{
		"access-key-id": "AK", "access-key-secret": "s", "sign-name": "x", "template-code": "SMS_1",
		"phones": "10000000000,13700000000,13800000000", "endpoint": srv.URL}}
	a := s.Send(context.Background(), ch, problem())
	if !a.OK || len(a.Bounces) != 1 || a.Bounces[0].Address != "10000000000" {
		t.Fatalf("%+v", a)
	}
}
