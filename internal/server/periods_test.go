package server

import "testing"

func TestPeriodsSavedFromTheConsole(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	if c := e.do("POST", "/api/v1/periods", map[string]string{"name": "office", "rule": "mon-fri 09:00-18:00; sat 10:00-12:00"}, nil); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if c := e.do("POST", "/api/v1/periods", map[string]string{"name": "bad", "rule": "someday 9-5"}, nil); c != 400 {
		t.Fatalf("bad rule accepted: %d", c)
	}
	var list struct {
		Items []struct{ Name, Rule string } `json:"items"`
	}
	e.do("GET", "/api/v1/periods", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "office" || list.Items[0].Rule != "mon-fri 09:00-18:00; sat 10:00-12:00" {
		t.Fatalf("list: %+v", list.Items)
	}
	// A period a channel uses cannot go.
	if c := e.do("POST", "/api/v1/channels", map[string]any{"name": "c1", "kind": "log", "opts": map[string]string{"period": "office"}}, nil); c != 200 {
		t.Fatalf("channel: %d", c)
	}
	if c := e.do("DELETE", "/api/v1/periods/office", nil, nil); c == 200 {
		t.Fatal("a period in use was deleted")
	}
	e.do("DELETE", "/api/v1/channels/c1", nil, nil)
	if c := e.do("DELETE", "/api/v1/periods/office", nil, nil); c != 200 {
		t.Fatalf("delete: %d", c)
	}
}
