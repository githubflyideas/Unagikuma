package app

import (
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/postmortem"
	"github.com/githubflyideas/unagikuma/internal/probe"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// CoverageOf says what a configured monitor checks, for the incident
// report; nil cfg or an unknown monitor gives an unknown coverage.
func CoverageOf(cfg *config.Config, monitor string) postmortem.Coverage {
	if cfg == nil || monitor == "" {
		return postmortem.Coverage{}
	}
	for _, m := range cfg.Monitors {
		if m.Name != monitor {
			continue
		}
		c := postmortem.Coverage{Known: true, Type: m.Written, Target: m.Target, Interval: m.Interval, Timeout: m.Timeout,
			Attempts: m.Attempts, Nodes: m.Nodes, Quorum: m.Quorum, Policy: m.Policy}
		if c.Type == "" {
			c.Type = m.Type
		}
		if c.Quorum == "" {
			c.Quorum = cfg.Quorum
		}
		if s, err := ProbeSpec(m, nil); err == nil {
			c.Depth = string(probe.DepthOf(s))
		}
		if m.Type == "PUSH" {
			c.Interval, c.Depth = m.Period, ""
		}
		return c
	}
	return postmortem.Coverage{}
}

// ReportLang is the language reports are written in: the one given, else
// the notification language.
func (a *App) ReportLang(lang string) string {
	if lang != "" {
		return lang
	}
	return a.messageLang()
}

// Postmortem builds the report of an incident.
func (a *App) Postmortem(id int64, lang string) (*postmortem.Report, error) {
	return a.PostmortemFor(id, lang, false)
}

// PostmortemFor is Postmortem; ownChangesOnly leaves out configuration
// changes that did not touch the incident's monitor.
func (a *App) PostmortemFor(id int64, lang string, ownChangesOnly bool) (*postmortem.Report, error) {
	return postmortem.Build(a.store, id, postmortem.Options{
		Lang: a.ReportLang(lang), Location: a.opts.Location, Now: time.Now(),
		Coverage: CoverageOf(a.cfg.current().Config, a.incidentMonitor(id)), OwnChangesOnly: ownChangesOnly,
	})
}

func (a *App) incidentMonitor(id int64) string {
	if inc, err := a.store.Incident(id); err == nil && inc != nil {
		return inc.Monitor
	}
	return ""
}

// PostmortemFrom builds a report straight from a data directory's store
// and configuration (the command line, with the server running or not).
func PostmortemFrom(st *store.Store, cfg *config.Config, id int64, lang string, loc *time.Location) (*postmortem.Report, error) {
	if lang == "" {
		lang, _ = st.Setting("message_lang")
	}
	if lang == "" {
		lang = "en"
	}
	mon := ""
	if inc, err := st.Incident(id); err == nil && inc != nil {
		mon = inc.Monitor
	}
	return postmortem.Build(st, id, postmortem.Options{Lang: lang, Location: loc, Now: time.Now(), Coverage: CoverageOf(cfg, mon)})
}
