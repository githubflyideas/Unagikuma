package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A webhook body copied from Uptime Kuma works unchanged.
func TestKumaWebhookBody(t *testing.T) {
	body := `{
"touser": "xxx",
"msgtype": "text",
"text": { "content": "{% if heartbeatJSON.status == 1 %}🟢 服务恢复{% else %}🔴 服务告警-Froa{% endif %}\n监控:{{ monitorJSON.name }}\n类型:{{ monitorJSON.type }}\n地址:{{ monitorJSON.hostname }}\n状态:{% if heartbeatJSON.status == 1 %}UP ✅{% else %}DOWN ❌{% endif %}\n信息:{{ heartbeatJSON.msg }}\n时间:{{ heartbeatJSON.localDateTime }}" }
}`
	cap := &capture{}
	srv := cap.server(t)
	o := map[string]string{"url": srv.URL, "content-type": "json", "body": body}
	if err := ValidateChannel("webhook", o); err != nil {
		t.Fatal(err)
	}
	if p := TemplateProblems("webhook", o); len(p) > 0 {
		t.Fatalf("template problems: %v", p)
	}
	for _, kind := range []string{"PROBLEM", "RECOVERY"} {
		m := problem()
		m.Kind = kind
		m.Detail = `offset "1.8s"`
		if a := (&Sender{}).Send(context.Background(), Channel{Name: "w", Kind: "webhook", Opts: o}, m); !a.OK {
			t.Fatalf("%s: %+v", kind, a)
		}
		_, got := cap.last()
		var v struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		if err := json.Unmarshal(got, &v); err != nil {
			t.Fatalf("%s: not JSON: %v\n%s", kind, err, got)
		}
		c := v.Text.Content
		want := map[string]string{"PROBLEM": "🔴 服务告警-Froa", "RECOVERY": "🟢 服务恢复"}[kind]
		if !strings.HasPrefix(c, want) || !strings.Contains(c, "监控:"+m.Title) || !strings.Contains(c, `信息:offset "1.8s"`) ||
			!strings.Contains(c, "地址:10.0.0.1") || strings.Contains(c, "{%") || !strings.Contains(c, "时间:20") {
			t.Fatalf("%s: content %q", kind, c)
		}
	}
}

func TestKumaToGo(t *testing.T) {
	for in, want := range map[string]string{
		"{% if heartbeatJSON.status == 1 %}up{% elsif msg != '' %}x{% else %}down{% endif %}": `{{if eq .heartbeatJSON.status 1}}up{{else if ne .msg ""}}x{{else}}down{{end}}`,
		"{{ name }} {{ STATUS }} {{.Name}}":                                                   "{{.name}} {{.STATUS}} {{.Name}}",
		"{% unless heartbeatJSON.status == 0 and msg %}ok{% endunless %}":                     "{{if not and (eq .heartbeatJSON.status 0) (.msg)}}ok{{end}}",
		"plain {{.Title}}": "plain {{.Title}}",
	} {
		if got := kumaToGo(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}
