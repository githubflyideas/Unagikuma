package server

import "testing"

func TestSavedAlertsListed(t *testing.T) {
	e := newEnv(t, testBind, nil)
	e.setup()
	st := e.app.Store()
	st.Saved("soft_filtered", "web", 1)
	st.Saved("cap_saved", "db", 2)
	st.Counter("outage_saved", 3) // an older count with no list
	st.Flush()
	var r struct {
		Days []struct {
			Day    string         `json:"day"`
			Counts map[string]int `json:"counts"`
		} `json:"days"`
		Items []struct {
			Name, Monitor string
			N             int
		} `json:"items"`
	}
	if c := e.do("GET", "/api/v1/saved-alerts", nil, &r); c != 200 {
		t.Fatalf("get: %d", c)
	}
	if len(r.Days) != 7 || len(r.Items) != 2 {
		t.Fatalf("%d days, items %+v", len(r.Days), r.Items)
	}
	today := r.Days[6].Counts
	if today["soft_filtered"] != 1 || today["cap_saved"] != 2 || today["outage_saved"] != 3 {
		t.Fatalf("today: %v", today)
	}
}
