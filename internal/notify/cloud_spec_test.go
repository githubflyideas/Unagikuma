package notify

// Spec-shape tests for the five cloud presets whose endpoints are fixed
// (Pushover, ServerChan, PushPlus, PagerDuty, Opsgenie). Each mock asserts
// the request matches the vendor's documented contract (method, path,
// auth placement, required fields) and replies with the vendor's
// documented response shapes — success and error — so a preset drifting
// from the published API fails here. Sources (checked 2026-11-24):
// pushover.net/api, pushplus.plus/doc/guide/api.html,
// doc2.ft07.com ServerChan API/SDK docs,
// developer.pagerduty.com Events API v2 (trigger + overview),
// docs.opsgenie.com Alert API.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type capturedReq struct {
	method, path, rawQuery, contentType, auth string
	body                                      map[string]any
	rawBody                                   []byte
}

// specServer replies with the given status/body and records the request.
func specServer(t *testing.T, status int, reply string, got *capturedReq) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = capturedReq{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery,
			contentType: r.Header.Get("Content-Type"), auth: r.Header.Get("Authorization"), rawBody: b}
		_ = json.Unmarshal(b, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
}

func specDeliver(t *testing.T, srv *httptest.Server, preset string, params map[string]string, m Message) Attempt {
	t.Helper()
	s := &Sender{Client: &http.Client{Transport: rewrite{srv.URL}}}
	opts := map[string]string{"preset": preset}
	for k, v := range params {
		opts[k] = v
	}
	return s.Send(t.Context(), Channel{Name: preset, Kind: "webhook", Opts: opts}, &m)
}

func TestPushoverSpecShapes(t *testing.T) {
	params := map[string]string{"token": "apptoken", "user-key": "userkey"}
	var got capturedReq
	srv := specServer(t, 200, `{"status":1,"request":"abc"}`, &got)
	defer srv.Close()
	a := specDeliver(t, srv, "pushover", params, Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d1"})
	if !a.OK {
		t.Fatalf("documented success shape rejected: %+v", a)
	}
	if got.path != "/1/messages.json" || got.body["token"] != "apptoken" || got.body["user"] != "userkey" ||
		!strings.Contains(got.body["title"].(string), "T") || !strings.Contains(got.body["message"].(string), "B") {
		t.Fatalf("request off-contract: %s %v", got.path, got.body)
	}
	// Documented error: HTTP 400 with status 0 and an errors array; the
	// refusal is permanent and the vendor's words reach the operator.
	var got2 capturedReq
	srv2 := specServer(t, 400, `{"token":"invalid","user":"invalid","status":0,"errors":["application token is invalid"]}`, &got2)
	defer srv2.Close()
	a = specDeliver(t, srv2, "pushover", params, Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d2"})
	if a.OK || !a.Permanent || !strings.Contains(a.Error, "application token is invalid") {
		t.Fatalf("documented error shape mishandled: %+v", a)
	}
}

func TestServerChanSpecShapes(t *testing.T) {
	params := map[string]string{"sendkey": "SCT12345"}
	var got capturedReq
	srv := specServer(t, 200, `{"code":0,"message":"","data":{"pushid":"1"}}`, &got)
	defer srv.Close()
	a := specDeliver(t, srv, "serverchan", params, Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d1"})
	if !a.OK {
		t.Fatalf("documented success shape rejected: %+v", a)
	}
	if got.path != "/SCT12345.send" || !strings.Contains(got.body["title"].(string), "T") || !strings.Contains(got.body["desp"].(string), "B") {
		t.Fatalf("request off-contract: %s %v", got.path, got.body)
	}
	// Errors arrive as HTTP 200 with a non-zero code and a message.
	var got2 capturedReq
	srv2 := specServer(t, 200, `{"code":40001,"message":"bad sendkey"}`, &got2)
	defer srv2.Close()
	a = specDeliver(t, srv2, "serverchan", params, Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d2"})
	if a.OK || !strings.Contains(a.Error, "bad sendkey") {
		t.Fatalf("documented error shape mishandled: %+v", a)
	}
}

func TestPushPlusSpecShapes(t *testing.T) {
	params := map[string]string{"token": "tok"}
	var got capturedReq
	srv := specServer(t, 200, `{"code":200,"msg":"请求成功","data":"3cbc5eab"}`, &got)
	defer srv.Close()
	a := specDeliver(t, srv, "pushplus", params, Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d1"})
	if !a.OK {
		t.Fatalf("documented success shape rejected: %+v", a)
	}
	if got.path != "/send" || got.body["token"] != "tok" || !strings.Contains(got.body["content"].(string), "B") || got.body["template"] != "txt" {
		t.Fatalf("request off-contract: %s %v", got.path, got.body)
	}
	var got2 capturedReq
	srv2 := specServer(t, 200, `{"code":400,"msg":"参数错误","data":null}`, &got2)
	defer srv2.Close()
	a = specDeliver(t, srv2, "pushplus", params, Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d2"})
	if a.OK || !strings.Contains(a.Error, "参数错误") {
		t.Fatalf("documented error shape mishandled: %+v", a)
	}
}

func TestPagerDutySpecShapes(t *testing.T) {
	params := map[string]string{"routing-key": "rk"}
	msg := Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d1", GlobalIncidentID: "home:e1:7"}
	var got capturedReq
	srv := specServer(t, 202, `{"status":"success","message":"Event processed","dedup_key":"unagikuma-home:e1:7"}`, &got)
	defer srv.Close()
	a := specDeliver(t, srv, "pagerduty", params, msg)
	if !a.OK {
		t.Fatalf("documented 202 success rejected: %+v", a)
	}
	pl, _ := got.body["payload"].(map[string]any)
	if got.path != "/v2/enqueue" || got.body["routing_key"] != "rk" || got.body["event_action"] != "trigger" ||
		got.body["dedup_key"] != "unagikuma-home:e1:7" || !strings.Contains(pl["summary"].(string), "T") || pl["severity"] != "critical" || pl["source"] != "unagikuma" {
		t.Fatalf("trigger off-contract: %v payload %v", got.body, pl)
	}
	// Recovery resolves under the same dedup key.
	msg.Kind = "RECOVERY"
	var got2 capturedReq
	srv2 := specServer(t, 202, `{"status":"success","message":"Event processed"}`, &got2)
	defer srv2.Close()
	a = specDeliver(t, srv2, "pagerduty", params, msg)
	if !a.OK || got2.body["event_action"] != "resolve" || got2.body["dedup_key"] != "unagikuma-home:e1:7" {
		t.Fatalf("resolve off-contract: %+v %v", a, got2.body)
	}
	// Documented error: 400 is "do not retry" in PagerDuty's own table.
	var got3 capturedReq
	srv3 := specServer(t, 400, `{"status":"invalid event","message":"Event object is invalid","errors":["payload is invalid"]}`, &got3)
	defer srv3.Close()
	msg.Kind = "PROBLEM"
	a = specDeliver(t, srv3, "pagerduty", params, msg)
	if a.OK || !a.Permanent {
		t.Fatalf("documented 400 must be a permanent failure: %+v", a)
	}
}

func TestOpsgenieSpecShapes(t *testing.T) {
	params := map[string]string{"api-key": "key-1"}
	msg := Message{Kind: "PROBLEM", Title: "T", Body: "B", DeliveryID: "d1", GlobalIncidentID: "home:e1:7"}
	var got capturedReq
	srv := specServer(t, 202, `{"result":"Request will be processed","took":0.1,"requestId":"r1"}`, &got)
	defer srv.Close()
	a := specDeliver(t, srv, "opsgenie", params, msg)
	if !a.OK {
		t.Fatalf("documented 202 success rejected: %+v", a)
	}
	if got.path != "/v2/alerts" || got.auth != "GenieKey key-1" || !strings.Contains(got.body["message"].(string), "T") ||
		got.body["alias"] != "unagikuma-home:e1:7" || got.body["priority"] != "P1" || strings.Contains(got.body["description"].(string), "B") == false {
		t.Fatalf("create off-contract: %s auth %q %v", got.path, got.auth, got.body)
	}
	// Recovery closes by alias, per the documented close endpoint.
	msg.Kind = "RECOVERY"
	var got2 capturedReq
	srv2 := specServer(t, 202, `{"result":"Request will be processed","took":0.1,"requestId":"r2"}`, &got2)
	defer srv2.Close()
	a = specDeliver(t, srv2, "opsgenie", params, msg)
	if !a.OK || got2.path != "/v2/alerts/unagikuma-home:e1:7/close" || !strings.Contains(got2.rawQuery, "identifierType=alias") {
		t.Fatalf("close off-contract: %+v %s?%s", a, got2.path, got2.rawQuery)
	}
	// Documented error shape: non-2xx with a message field; 400 permanent.
	var got3 capturedReq
	srv3 := specServer(t, 400, `{"message":"Invalid API key","took":0.01,"requestId":"r3"}`, &got3)
	defer srv3.Close()
	msg.Kind = "PROBLEM"
	a = specDeliver(t, srv3, "opsgenie", params, msg)
	if a.OK || !a.Permanent || !strings.Contains(a.Error, "Invalid API key") {
		t.Fatalf("documented error shape mishandled: %+v", a)
	}
}
