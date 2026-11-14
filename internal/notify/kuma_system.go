package notify

import (
	"bytes"
	"encoding/json"
	"strings"
)

// A body template pasted from Uptime Kuma describes a monitor going down
// or up: "Status: {% if heartbeatJSON.status == 1 %}UP{% else %}DOWN{%
// endif %}", the monitor's type and address. A message that is about no
// monitor (the daily report, a change notice) would come out as an empty
// monitor that is DOWN. Such a message keeps the request the service
// expects, but the template's text fields carry the message's own text.

// kumaMark stands for a Kuma value while the template is examined.
const kumaMark = "\u2063kuma\u2063"

// systemKumaBody renders a Kuma-style body template for a message about no
// monitor. ok is false when the template's result is not JSON whose text
// fields can be found; the caller then renders it as before.
func systemKumaBody(tpl string, m *Message, o map[string]string) (string, bool) {
	title, body := textOf(o, m)
	text := strings.TrimRight(title+"\n"+body, "\n")
	// Render twice, up and down, with every Kuma value a mark: a field that
	// carries a mark, or reads differently up and down, is message text.
	render := func(status int) (any, bool) {
		d := templateData(m, o, true)
		mon := map[string]any{}
		for _, k := range []string{"id", "name", "type", "url", "hostname", "port", "description"} {
			mon[k] = kumaMark
		}
		d["monitorJSON"] = mon
		d["heartbeatJSON"] = map[string]any{"status": status, "msg": kumaMark, "time": kumaMark,
			"localDateTime": kumaMark, "timezone": kumaMark, "important": true}
		for _, k := range []string{"msg", "name", "status", "hostnameOrURL", "NAME", "STATUS", "HOSTNAME_OR_URL"} {
			d[k] = kumaMark
		}
		out, err := renderTemplate(tpl, d)
		if err != nil {
			return nil, false
		}
		dec := json.NewDecoder(strings.NewReader(out))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) != nil {
			return nil, false
		}
		return v, true
	}
	down, ok1 := render(0)
	up, ok2 := render(1)
	if !ok1 || !ok2 {
		return "", false
	}
	var leaves []*string
	var longest *string
	var walk func(a, b any) any
	walk = func(a, b any) any {
		switch x := a.(type) {
		case map[string]any:
			y, _ := b.(map[string]any)
			for k, v := range x {
				x[k] = walk(v, y[k])
			}
		case []any:
			y, _ := b.([]any)
			for i, v := range x {
				var w any
				if i < len(y) {
					w = y[i]
				}
				x[i] = walk(v, w)
			}
		case string:
			if strings.Contains(x, kumaMark) || x != b {
				s := x
				leaves = append(leaves, &s)
				if longest == nil || len(s) > len(*longest) {
					longest = &s
				}
				return &s
			}
		}
		return a
	}
	tree := walk(down, up)
	if longest == nil {
		return "", false
	}
	// The longest text field gets the whole message, any other (a title)
	// the first line.
	for _, p := range leaves {
		if p == longest {
			*p = text
		} else {
			*p = title
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(deref(tree)) != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

// deref replaces the string pointers walk left with their strings.
func deref(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, w := range x {
			x[k] = deref(w)
		}
	case []any:
		for i, w := range x {
			x[i] = deref(w)
		}
	case *string:
		return *x
	}
	return v
}
