package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A delay under a minute is real and must be stated in seconds, never
// truncated to "[delayed 0 min]". Minute-scale delays keep the minute
// form (truncation there understates, never invents).
func TestDelayedPrefix(t *testing.T) {
	m := problem()
	m.Lang, m.Delayed = "en", 30*time.Second
	if title, _ := Render(m); !strings.Contains(title, "[delayed 30 s]") || strings.Contains(title, "0 min") {
		t.Errorf("sub-minute delay: %q", title)
	}
	m.Delayed = 5*time.Minute + 59*time.Second
	if title, _ := Render(m); !strings.Contains(title, "[delayed 5 min]") {
		t.Errorf("minute delay: %q", title)
	}
	m.Lang, m.Delayed = "zh", 45*time.Second
	if title, _ := Render(m); !strings.Contains(title, "[延迟 45 秒]") {
		t.Errorf("zh sub-minute delay: %q", title)
	}
}

// The webhook payload no longer carries a "quorum" key (the field was
// never populated; the observations snapshot is the real cluster data),
// and templates no longer see the Quorum or AckURL names.
func TestNoQuorumOrAckURL(t *testing.T) {
	cap := &capture{resp: `{}`}
	srv := cap.server(t)
	if a := send(t, "webhook", map[string]string{"url": srv.URL}, problem()); !a.OK {
		t.Fatal(a)
	}
	_, body := cap.last()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("invalid JSON %s", body)
	}
	if _, ok := m["quorum"]; ok {
		t.Errorf("payload still carries quorum: %s", body)
	}
	d := messageData(problem(), nil)
	for _, k := range []string{"Quorum", "AckURL"} {
		if _, ok := d[k]; ok {
			t.Errorf("template data still has %s", k)
		}
	}
	// Compat, pinned: a stored template still naming a removed field
	// renders it as empty at send time (missingkey=zero), and is refused
	// at configuration-check time like any other unknown name.
	out, err := renderTemplate("q=[{{.Quorum}}] a=[{{.AckURL}}]", d)
	if err != nil || out != "q=[] a=[]" {
		t.Errorf("removed names at send time: %q, %v", out, err)
	}
	if err := checkTemplate("template", "{{.Quorum}}", nil); err == nil {
		t.Error("checkTemplate must refuse the removed Quorum name")
	}
	if err := checkTemplate("template", "{{.AckURL}}", nil); err == nil {
		t.Error("checkTemplate must refuse the removed AckURL name")
	}
}
