package server

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Acknowledging twice keeps both notes, and the first ACK's time.
func TestAckHistoryIsAppendOnly(t *testing.T) {
	e := newEnv(t, testBind+"down IN TCP 127.0.0.1:1\n", nil)
	e.setup()
	type list struct {
		Items []struct {
			ID      int64
			Monitor string
			State   map[string]any
			Acks    []map[string]any
		}
	}
	var id int64
	for deadline := time.Now().Add(15 * time.Second); id == 0; time.Sleep(100 * time.Millisecond) {
		var l list
		e.do("GET", "/api/v1/incidents?state=unhandled", nil, &l)
		for _, it := range l.Items {
			if it.Monitor == "down" {
				id = it.ID
			}
		}
		if id == 0 && time.Now().After(deadline) {
			var r struct{ Row map[string]any }
			e.do("GET", "/api/v1/monitors/down", nil, &r)
			var all map[string]any
			e.do("GET", "/api/v1/incidents?state=all", nil, &all)
			t.Fatalf("no incident for down: %v\n%v", r.Row, all)
		}
	}
	ack := func(comment string) {
		if code := e.do("POST", fmt.Sprintf("/api/v1/incidents/%d/ack", id), map[string]any{"comment": comment}, nil); code != 204 && code != 200 {
			t.Fatalf("ack: %d", code)
		}
	}
	ack("looking into it")
	var l list
	e.do("GET", "/api/v1/incidents?state=handled", nil, &l)
	first := l.Items[0].State["first_acked_at"]
	time.Sleep(1100 * time.Millisecond)
	ack("switch replaced, waiting for links")
	e.do("GET", "/api/v1/incidents?state=handled", nil, &l)
	it := l.Items[0]
	if len(it.Acks) != 2 || it.Acks[0]["comment"] != "looking into it" || it.Acks[1]["comment"] != "switch replaced, waiting for links" || it.Acks[0]["via"] != "web" {
		t.Fatalf("ack history: %v", it.Acks)
	}
	if first == nil || it.State["first_acked_at"] != first {
		t.Fatalf("first ack time moved: %v → %v", first, it.State["first_acked_at"])
	}

	// The incident's report tells the story, with both acknowledgements.
	var rep struct {
		Conclusion string
		Segments   []struct {
			Key     string
			Entries []struct{ What string }
		}
		CoverageText []string `json:"coverage_text"`
	}
	if code := e.do("GET", fmt.Sprintf("/api/v1/incidents/%d/report?lang=en", id), nil, &rep); code != 200 {
		t.Fatalf("report: %d", code)
	}
	if len(rep.Segments) != 4 || rep.Conclusion == "" || len(rep.CoverageText) == 0 || !strings.Contains(rep.CoverageText[0], "127.0.0.1:1") {
		t.Fatalf("report: %+v", rep)
	}
	acks := 0
	for _, en := range rep.Segments[2].Entries {
		if strings.Contains(en.What, "acknowledged by") {
			acks++
		}
	}
	if acks != 2 {
		t.Errorf("notification segment: %+v", rep.Segments[2])
	}
	if code := e.do("GET", fmt.Sprintf("/api/v1/incidents/%d/report?format=pdf", id), nil, nil); code != 400 {
		t.Errorf("unknown format: %d", code)
	}
	if code := e.do("GET", "/api/v1/incidents/99999/report", nil, nil); code != 404 {
		t.Errorf("unknown incident: %d", code)
	}
}

// Silence takes its body flat or wrapped in "record", like the other write
// endpoints; the two forms disagreeing is refused.
func TestSilenceBodyShapes(t *testing.T) {
	e := newEnv(t, testBind+"down IN TCP 127.0.0.1:1\n", nil)
	e.setup()
	var id int64
	for deadline := time.Now().Add(15 * time.Second); id == 0 && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		var l struct {
			Items []struct {
				ID      int64
				Monitor string
			}
		}
		e.do("GET", "/api/v1/incidents?state=unhandled", nil, &l)
		for _, it := range l.Items {
			if it.Monitor == "down" {
				id = it.ID
			}
		}
	}
	if id == 0 {
		t.Fatal("no incident")
	}
	path := fmt.Sprintf("/api/v1/incidents/%d/silence", id)
	for _, body := range []map[string]any{
		{"duration_s": 600},
		{"record": map[string]any{"duration_s": 900}},
		{"duration_s": 900, "record": map[string]any{"duration_s": 900}},
	} {
		if code := e.do("POST", path, body, nil); code != 204 && code != 200 {
			t.Fatalf("silence %v: %d", body, code)
		}
	}
	if code := e.do("POST", path, map[string]any{"duration_s": 60, "record": map[string]any{"duration_s": 900}}, nil); code != 400 {
		t.Fatalf("two different durations: %d", code)
	}
}
