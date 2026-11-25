package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// RequestPreview is the HTTP request a channel would send for an example
// problem, with its credentials masked, so the form can show it in the
// receiving service's own format.
type RequestPreview struct {
	Method  string     `json:"method"`
	URL     string     `json:"url"`
	Query   [][]string `json:"query"`
	Headers [][]string `json:"headers"`
	Body    string     `json:"body"`
	Title   string     `json:"title"`
	Text    string     `json:"text"`
}

// credentialName matches parameter and header names that carry a
// credential.
var credentialName = regexp.MustCompile(`(?i)^(signature|sig|sign|key|access_?key(_?id)?|account_?sid|sid)$|authorization|token|secret|password|api-?key|auth-?key`)

// ErrNoHTTP means the channel does not send an HTTP request (e-mail, log).
var ErrNoHTTP = errors.New("this channel does not send an HTTP request")

// PreviewRequest builds the request a channel sends for an example problem
// without sending it: the same code as a real delivery runs against a
// transport that records the first request and answers it itself. Fields
// still empty in the form get stand-ins, so the format shows from the
// start.
func PreviewRequest(kind string, opts map[string]string, lang, instance string) (*RequestPreview, error) {
	ch := Channel{Name: "preview", Kind: kind, Opts: map[string]string{}}
	if p, ok := strings.CutPrefix(kind, "webhook:"); ok {
		ch.Kind = "webhook"
		ch.Opts["preset"] = p
	}
	for k, v := range opts {
		ch.Opts[k] = v
	}
	fillStandIns(kind, ch.Opts)
	m := sampleMessage(&Message{Lang: lang, Instance: instance})
	title, text := textOf(ch.Opts, m)
	pv := &RequestPreview{Title: title, Text: text}
	if ch.Kind == "email" || ch.Kind == "log" || ch.Kind == "group" {
		return pv, ErrNoHTTP
	}
	rt := &recorder{}
	s := &Sender{Client: &http.Client{Transport: rt}, Instance: instance}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a := s.Send(ctx, ch, m)
	req, body := rt.first()
	if req == nil {
		if a.Error != "" {
			return pv, errors.New(a.Error)
		}
		return pv, ErrNoHTTP
	}
	// The kind's own secret fields count too (a preset's webhook address).
	secrets := secretsOf(ch)
	for _, k := range Kinds() {
		if k.Kind != kind {
			continue
		}
		for _, f := range k.Fields {
			if v := ch.Opts[f.Key]; f.Type == "secret" && v != "" {
				secrets = append(secrets, v)
				if pu, err := url.Parse(v); err == nil && strings.Trim(pu.Path, "/") != "" {
					secrets = append(secrets, strings.Trim(pu.Path, "/"))
				}
			}
		}
	}
	pv.Method = req.Method
	// The address up to its path, then each parameter: a credential in the
	// path or a parameter that carries one shows as ***.
	u := *req.URL
	u.RawQuery, u.User = "", nil
	pv.URL = u.Scheme + "://" + u.Host + maskPath(u.EscapedPath(), secrets)
	qs := req.URL.Query()
	qnames := make([]string, 0, len(qs))
	for k := range qs {
		qnames = append(qnames, k)
	}
	sort.Strings(qnames)
	for _, k := range qnames {
		v := Redact(qs.Get(k), secrets)
		if credentialName.MatchString(k) {
			v = "***"
		}
		pv.Query = append(pv.Query, []string{k, v})
	}
	names := make([]string, 0, len(req.Header))
	for k := range req.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if k == "User-Agent" || k == "X-Unagikuma-Delivery-Id" {
			continue
		}
		v := Redact(req.Header.Get(k), secrets)
		if credentialName.MatchString(k) {
			if scheme, _, ok := strings.Cut(v, " "); ok && len(scheme) < 12 {
				v = scheme + " ***"
			} else {
				v = "***"
			}
		}
		pv.Headers = append(pv.Headers, []string{k, v})
	}
	pv.Body = Redact(prettyJSON(body), secrets)
	return pv, nil
}

// fillStandIns gives empty or hidden fields a value that shows the format.
func fillStandIns(kind string, o map[string]string) {
	for _, k := range Kinds() {
		if k.Kind != kind {
			continue
		}
		for _, f := range k.Fields {
			v := o[f.Key]
			if v != "" && v != "***" && !strings.Contains(v, "${") {
				continue
			}
			if v == "" && !f.Required && f.Type != "url" && f.Type != "secret" {
				continue
			}
			switch {
			case strings.HasPrefix(f.Placeholder, "http"):
				o[f.Key] = strings.ReplaceAll(strings.ReplaceAll(f.Placeholder, "…", "***"), "...", "***")
			case f.Default != "":
				o[f.Key] = f.Default
			case f.Type == "url" || f.Key == "url":
				o[f.Key] = "https://hooks.example.com/***"
			case f.Type == "secret":
				o[f.Key] = "secret-value"
			case f.Placeholder != "":
				o[f.Key] = strings.TrimSpace(strings.SplitN(f.Placeholder, ",", 2)[0])
			case f.Key == "to" || f.Key == "phones":
				o[f.Key] = "+8613800000000"
			default:
				o[f.Key] = "example"
			}
		}
	}
}

func prettyJSON(b []byte) string {
	var out bytes.Buffer
	if json.Valid(b) && json.Indent(&out, b, "", "  ") == nil {
		return out.String()
	}
	return string(b)
}

// recorder answers every request itself, keeping the first one.
type recorder struct {
	mu   sync.Mutex
	req  *http.Request
	body []byte
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	if r.req == nil {
		r.req = req
		if req.Body != nil {
			r.body, _ = io.ReadAll(req.Body)
		}
	}
	r.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"ok":true,"code":0,"errcode":0,"StatusCode":0,"status":1}`)), Request: req}, nil
}

func (r *recorder) first() (*http.Request, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.req, r.body
}

// maskPath hides the parts of an address path that come from a credential
// (a webhook token, a bot token) and keeps the service's own path, so the
// official format still shows: /open-apis/bot/v2/hook/***.
func maskPath(path string, secrets []string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		for _, sec := range secrets {
			switch {
			case len(seg) >= 16 && strings.Contains(sec, seg):
				segs[i] = "***" // a token that is the whole segment
			case len(sec) >= 8 && !strings.Contains(sec, "/") && strings.Contains(seg, sec):
				segs[i] = strings.ReplaceAll(seg, sec, "***") // bot<token>
			}
		}
	}
	return strings.Join(segs, "/")
}
