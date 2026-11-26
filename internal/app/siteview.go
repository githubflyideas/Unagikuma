package app

import (
	"math"
	"time"

	"github.com/githubflyideas/unagikuma/internal/observation"
)

// The site view — what every other site of a cluster sees of a monitor
// (✓ ✗ ⊘ in the console, the sites line in an alert) — is registered,
// with a free trial: SiteViewTrialDays days from the first time it is
// asked for in a cluster. Only the showing is limited: the sites still
// check, exchange their observations and ask each other to recheck, so
// nothing about alerting changes, and registering shows it at once.

const SiteViewTrialDays = 7

const siteViewTrialKey = "site_view.trial_start"

// SiteViewTrial is where the installation stands with the site view.
type SiteViewTrial struct {
	Allowed  bool `json:"allowed"`
	Trial    bool `json:"trial"`
	DaysLeft int  `json:"days_left,omitempty"`
	Ended    bool `json:"ended,omitempty"`
}

// SiteView reports whether the site view is shown, starting the trial the
// first time it is asked for.
func (a *App) SiteView() SiteViewTrial {
	if a.License().State().Registered {
		return SiteViewTrial{Allowed: true}
	}
	v, _ := a.store.Setting(siteViewTrialKey)
	start, err := time.Parse(time.RFC3339, v)
	if v == "" || err != nil {
		start = a.now().UTC()
		_ = a.store.SetSetting(siteViewTrialKey, start.Format(time.RFC3339))
	}
	end := start.AddDate(0, 0, SiteViewTrialDays)
	if !a.now().Before(end) {
		return SiteViewTrial{Ended: true}
	}
	return SiteViewTrial{Allowed: true, Trial: true, DaysLeft: int(math.Ceil(end.Sub(a.now()).Hours() / 24))}
}

// SiteObservationSnapshot is what an alert carries about the other sites:
// nothing when the site view is not allowed.
func (s *sink) SiteObservationSnapshot(monitor, defhash string, sites []string, now time.Time) (*observation.Snapshot, error) {
	if !s.a.SiteView().Allowed {
		return nil, nil
	}
	return s.Store.SiteObservationSnapshot(monitor, defhash, sites, now)
}
