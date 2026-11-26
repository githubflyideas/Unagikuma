package app

import (
	"testing"
	"time"
)

// The site view starts its trial the first time it is asked for, runs
// SiteViewTrialDays days and then stops showing; nothing else changes.
func TestSiteViewTrial(t *testing.T) {
	a, _, _, _ := newApp(t, testConfig)
	defer a.Store().Close()
	v := a.SiteView()
	if !v.Allowed || !v.Trial || v.DaysLeft != SiteViewTrialDays {
		t.Fatalf("first ask: %+v", v)
	}
	start := time.Now().UTC().AddDate(0, 0, -SiteViewTrialDays).Add(-time.Minute)
	if err := a.Store().SetSetting(siteViewTrialKey, start.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if v := a.SiteView(); v.Allowed || !v.Ended {
		t.Fatalf("after the trial: %+v", v)
	}
	s := &sink{a: a, Store: a.Store()}
	if snap, err := s.SiteObservationSnapshot("web", "x", []string{"b"}, time.Now()); snap != nil || err != nil {
		t.Fatalf("an alert after the trial carries the sites: %v %v", snap, err)
	}
}
