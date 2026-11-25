package app

import (
	"math"
	"time"
)

// The registered styles are free to try: StyleTrialDays days from the
// first time one of them is switched on (set as the installation
// default, or picked for an account). The start is kept with the
// installation's settings, so it survives restarts and is the same
// for every account. Registering ends the question: a registered
// installation never looks at the trial.

const StyleTrialDays = 7

const styleTrialKey = "style.trial_start"

// StyleTrial is where the installation stands with the style trial.
type StyleTrial struct {
	Started  bool `json:"started"`
	Active   bool `json:"active"`
	Ended    bool `json:"ended"`
	DaysLeft int  `json:"days_left"`
}

// StyleTrial reports the trial state now.
func (a *App) StyleTrial() StyleTrial {
	v, _ := a.store.Setting(styleTrialKey)
	if v == "" {
		return StyleTrial{}
	}
	start, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return StyleTrial{}
	}
	end := start.AddDate(0, 0, StyleTrialDays)
	t := StyleTrial{Started: true, Active: a.now().Before(end)}
	t.Ended = !t.Active
	if t.Active {
		t.DaysLeft = int(math.Ceil(end.Sub(a.now()).Hours() / 24))
	}
	return t
}

// StartStyleTrial notes the first activation of a registered style;
// later activations change nothing.
func (a *App) StartStyleTrial() {
	if v, _ := a.store.Setting(styleTrialKey); v != "" {
		return
	}
	_ = a.store.SetSetting(styleTrialKey, a.now().UTC().Format(time.RFC3339))
}
