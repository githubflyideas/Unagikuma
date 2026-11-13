package notify

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Custom SMS and voice channels send through a gateway of the operator's
// own (a carrier's HTTP interface, an in-house SMS service): one request
// per number, whose URL, headers and body are templates with {{.Phone}}
// and every name of a message template.

// customDefaultBody is the request body when body= is not set: what most
// gateways take.
const customDefaultBody = `{"phone":"{{.Phone}}","message":"{{.Title}}"}`

var customFields = []Field{
	{Key: "url", Type: "url", Required: true, Placeholder: "https://sms.example.com/send?to={{.Phone}}"},
	{Key: "phones", Type: "text", Required: true, Placeholder: "13800000000,13900000000"},
	{Key: "method", Type: "select", Options: []string{"POST", "PUT", "GET"}, Default: "POST"},
	{Key: "content-type", Type: "select", Options: []string{"json", "form", "text"}, Default: "json"},
	{Key: "body", Type: "textarea", Placeholder: customDefaultBody},
	{Key: "success-match", Type: "text", Placeholder: `"code":0`},
	tplField, prefixField,
}

func init() {
	nativeKinds = append(nativeKinds,
		KindInfo{Kind: "custom-sms", Label: "Custom SMS gateway", Fields: customFields},
		KindInfo{Kind: "custom-voice", Label: "Custom voice gateway", Fields: customFields})
}

func isCustomKind(kind string) bool { return kind == "custom-sms" || kind == "custom-voice" }

// sendCustom sends to every number, one request each; it succeeds when
// one number got it, as the provider channels do.
func (s *Sender) sendCustom(ctx context.Context, ch Channel, m *Message) ([]Bounce, error) {
	phones := phonesOf(ch.Opts)
	if len(phones) == 0 {
		return nil, errors.New("no phone numbers")
	}
	var errs []string
	agg := failure{permanent: true}
	sent := 0
	for _, p := range phones {
		o := make(map[string]string, len(ch.Opts)+1)
		for k, v := range ch.Opts {
			o[k] = v
		}
		if o["body"] == "" && !strings.EqualFold(o["method"], http.MethodGet) {
			o["body"] = customDefaultBody
		}
		o["Phone"] = p
		req, check, err := s.buildWebhook(ctx, o, m)
		if err == nil {
			a := s.do(req, check, Attempt{})
			if a.OK {
				sent++
				continue
			}
			err = &failure{msg: a.Error, permanent: a.Permanent, retryAfter: a.RetryAfter}
		}
		var f *failure
		if errors.As(err, &f) {
			agg.permanent = agg.permanent && f.permanent
			agg.retryAfter = max(agg.retryAfter, f.retryAfter)
		} else {
			agg.permanent = false
		}
		errs = append(errs, p+": "+err.Error())
	}
	if sent == 0 {
		agg.msg = strings.Join(errs, "; ")
		return nil, &agg
	}
	return nil, nil
}
