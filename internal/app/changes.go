package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/schedule"
	"github.com/githubflyideas/unagikuma/internal/store"
	"github.com/githubflyideas/unagikuma/internal/tunables"
)

// Announced changes: planned work is told to the people concerned when it
// is announced, reminded before it starts, held as a maintenance window
// for the monitors it touches, and closed with a receipt (done or rolled
// back). Every notice goes through the usual delivery and is recorded.

// Change notice events.
const (
	ChangeAnnounced   = "announced"
	ChangeRescheduled = "rescheduled"
	ChangeRemind      = "remind"
	ChangeStarted     = "started"
	ChangeOverdue     = "overdue"
	ChangeFinished    = "done"
	ChangeRolledBack  = "rolled_back"
	ChangeCancelled   = "cancelled"
)

// ChangeEvents are the notices a change can send, in order.
var ChangeEvents = []string{ChangeAnnounced, ChangeRescheduled, ChangeRemind, ChangeStarted, ChangeOverdue, ChangeFinished, ChangeRolledBack, ChangeCancelled}

// ChangeTemplate is the title and body of one notice, as Go templates.
type ChangeTemplate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

const enWindow = `When: {{.Start}} – {{.End}} ({{.Duration}})
{{- if .Impact}}
Impact: {{.Impact}}{{end}}
{{- if .Description}}
{{.Description}}{{end}}
{{- if .Monitors}}
In maintenance: {{.Monitors}}{{end}}`

const zhWindow = `时间：{{.Start}} – {{.End}}（{{.Duration}}）
{{- if .Impact}}
影响：{{.Impact}}{{end}}
{{- if .Description}}
{{.Description}}{{end}}
{{- if .Monitors}}
维护中的监控：{{.Monitors}}{{end}}`

// defaultChangeTemplates are the built-in notices; other languages use
// the English ones. Each can be replaced under Notices & status › Changes.
var defaultChangeTemplates = map[string]map[string]ChangeTemplate{
	"en": {
		ChangeAnnounced:   {"Planned change: {{.Title}}", enWindow + "\nAnnounced by {{.By}}."},
		ChangeRescheduled: {"Change rescheduled: {{.Title}}", enWindow},
		ChangeRemind:      {"In {{.In}}: {{.Title}}", enWindow},
		ChangeStarted:     {"Change started: {{.Title}}", enWindow},
		ChangeOverdue:     {"Change overdue: {{.Title}}", "It was due to end at {{.End}} and is not marked as done or rolled back. Its monitors are out of maintenance again."},
		ChangeFinished:    {"Change done: {{.Title}}", "Done at {{.Now}} by {{.By}}.{{if .Note}}\n{{.Note}}{{end}}"},
		ChangeRolledBack:  {"Change rolled back: {{.Title}}", "Rolled back at {{.Now}} by {{.By}}.{{if .Note}}\n{{.Note}}{{end}}"},
		ChangeCancelled:   {"Change cancelled: {{.Title}}", "Planned for {{.Start}}; cancelled by {{.By}}.{{if .Note}}\n{{.Note}}{{end}}"},
	},
	"zh": {
		ChangeAnnounced:   {"变更通知：{{.Title}}", zhWindow + "\n发布人：{{.By}}"},
		ChangeRescheduled: {"变更时间调整：{{.Title}}", zhWindow},
		ChangeRemind:      {"{{.In}}后开始：{{.Title}}", zhWindow},
		ChangeStarted:     {"变更开始：{{.Title}}", zhWindow},
		ChangeOverdue:     {"变更超时：{{.Title}}", "原定 {{.End}} 结束，但还没有标记为完成或已回滚。相关监控已退出维护状态。"},
		ChangeFinished:    {"变更完成：{{.Title}}", "{{.By}} 于 {{.Now}} 完成。{{if .Note}}\n{{.Note}}{{end}}"},
		ChangeRolledBack:  {"变更已回滚：{{.Title}}", "{{.By}} 于 {{.Now}} 回滚。{{if .Note}}\n{{.Note}}{{end}}"},
		ChangeCancelled:   {"变更取消：{{.Title}}", "原定 {{.Start}}，已由 {{.By}} 取消。{{if .Note}}\n{{.Note}}{{end}}"},
	},
}

const changeTemplatesSetting = "change_templates"

// ChangeTemplates returns the templates in use for a language: the
// built-in ones with the saved replacements over them.
func (a *App) ChangeTemplates(lang string) (map[string]ChangeTemplate, map[string]bool) {
	base := defaultChangeTemplates["en"]
	if t, ok := defaultChangeTemplates[strings.SplitN(lang, "-", 2)[0]]; ok {
		base = t
	}
	out := map[string]ChangeTemplate{}
	for k, v := range base {
		out[k] = v
	}
	custom := map[string]bool{}
	if raw, _ := a.store.Setting(changeTemplatesSetting); raw != "" {
		var saved map[string]ChangeTemplate
		if json.Unmarshal([]byte(raw), &saved) == nil {
			for k, v := range saved {
				if _, ok := out[k]; ok && (v.Title != "" || v.Body != "") {
					out[k], custom[k] = v, true
				}
			}
		}
	}
	return out, custom
}

// SetChangeTemplates saves replacements (an empty template restores the
// built-in one). Every template must parse.
func (a *App) SetChangeTemplates(t map[string]ChangeTemplate) error {
	keep := map[string]ChangeTemplate{}
	for k, v := range t {
		known := false
		for _, e := range ChangeEvents {
			known = known || e == k
		}
		if !known {
			return fmt.Errorf("unknown notice %q", k)
		}
		if v.Title == "" && v.Body == "" {
			continue
		}
		for _, s := range []string{v.Title, v.Body} {
			if _, err := template.New("t").Option("missingkey=zero").Parse(s); err != nil {
				return fmt.Errorf("%s: %v", k, err)
			}
		}
		keep[k] = v
	}
	b, _ := json.Marshal(keep)
	return a.store.SetSetting(changeTemplatesSetting, string(b))
}

// changeMonitors resolves a change's selector against the configuration.
func changeMonitors(cfg *config.Config, sel string) []string {
	if cfg == nil || strings.TrimSpace(sel) == "" {
		return nil
	}
	s, err := config.ParseSelector(sel)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range cfg.Monitors {
		if s.Match(m) {
			out = append(out, m.Name)
		}
	}
	return out
}

// RenderChange renders one notice of a change.
func (a *App) RenderChange(c store.Change, event, by, in string) (title, body string, err error) {
	lang := a.messageLang()
	tpls, _ := a.ChangeTemplates(lang)
	tpl := tpls[event]
	loc := a.opts.Location
	f := func(t time.Time) string { return t.In(loc).Format("2006-01-02 15:04") }
	mons := changeMonitors(a.applied.Load(), c.Select)
	if len(mons) > 20 {
		mons = append(mons[:20], fmt.Sprintf("… (+%d)", len(mons)-20))
	}
	data := map[string]any{"Title": c.Title, "Description": c.Description, "Impact": c.Impact, "Start": f(c.Start), "End": f(c.End),
		"Duration": shortDuration(c.End.Sub(c.Start)), "Monitors": strings.Join(mons, ", "), "Note": c.Note, "By": by, "In": in,
		"Now": f(a.now()), "State": c.State}
	render := func(s string) (string, error) {
		t, err := template.New("t").Option("missingkey=zero").Parse(s)
		if err != nil {
			return "", err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, data); err != nil {
			return "", err
		}
		return strings.TrimSpace(b.String()), nil
	}
	if title, err = render(tpl.Title); err != nil {
		return "", "", err
	}
	body, err = render(tpl.Body)
	return title, body, err
}

// NotifyChange sends one notice of a change to its audience.
func (a *App) NotifyChange(c store.Change, event, by, in string) error {
	title, body, err := a.RenderChange(c, event, by, in)
	if err != nil {
		return err
	}
	policy := c.Policy
	if policy == "" && len(c.Channels) == 0 {
		if cfg := a.applied.Load(); cfg != nil {
			policy = cfg.Tunables.Raw("changes.policy")
		}
	}
	a.engine.Announce("CHANGE", policy, c.Channels, title, body)
	return nil
}

// ChangesChanged re-applies the configuration with the changes' windows.
func (a *App) ChangesChanged() {
	a.reapply()
	a.emit("changes", nil)
}

// reapply applies the running configuration again (after a change of
// state kept outside monitors.conf: the directory, announced changes).
func (a *App) reapply() {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	// Read under the lock: a configuration applied meanwhile is the one
	// to re-apply, never an older one.
	if c := a.applied.Load(); c != nil {
		a.applyLocked(c)
	}
}

// changeWindows are the maintenance windows of the open changes.
func (a *App) changeWindows(cfg *config.Config) []engine.MaintSpec {
	open, err := a.store.Changes(store.ChangeFilter{State: "open", Limit: 1000})
	if err != nil {
		a.log.Warn("changes", "err", err)
		return nil
	}
	var out []engine.MaintSpec
	for _, c := range open {
		mons := changeMonitors(cfg, c.Select)
		if len(mons) == 0 {
			continue
		}
		out = append(out, engine.MaintSpec{Name: fmt.Sprintf("change-%d", c.ID), Title: c.Title, Monitors: mons,
			Schedule: schedule.Spec{At: c.Start, Duration: c.End.Sub(c.Start)}})
	}
	return out
}

// reminders are the offsets before the start at which reminders go out.
func reminders(ts tunables.Set) []time.Duration {
	var out []time.Duration
	for _, s := range strings.Split(ts.Raw("changes.reminders"), ",") {
		if d, err := tunables.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// tickChanges sends what is due: reminders, the start, the overdue notice.
func (a *App) tickChanges() {
	cfg := a.applied.Load()
	if cfg == nil || a.SupersededBy() != nil {
		return
	}
	open, err := a.store.Changes(store.ChangeFilter{State: "open", Limit: 1000})
	if err != nil {
		a.log.Warn("changes", "err", err)
		return
	}
	now := a.now()
	send := func(c store.Change, key, event, in string) {
		// Recorded first: a crash in between loses a notice rather than
		// sending it twice.
		if ok, err := a.store.MarkChangeSent(c.ID, key); err != nil || !ok {
			return
		}
		if err := a.NotifyChange(c, event, c.CreatedBy, in); err != nil {
			a.log.Warn("change notice", "change", c.ID, "event", event, "err", err)
		}
	}
	changed := false
	for _, c := range open {
		if c.State == store.ChangePlanned {
			for _, d := range reminders(cfg.Tunables) {
				at := c.Start.Add(-d)
				// Only reminders that come after the announcement itself.
				if !now.Before(at) && now.Before(c.Start) && c.Created.Before(at) {
					send(c, "remind-"+d.String(), ChangeRemind, humanDuration(d, a.messageLang()))
				}
			}
			if !now.Before(c.Start) {
				if nc, err := a.store.SetChangeState(c.ID, store.ChangeRunning, "", ""); err == nil {
					c, changed = nc, true
					if now.Before(c.End) {
						send(c, "started", ChangeStarted, "")
					}
				}
			}
		}
		if c.State == store.ChangeRunning && !now.Before(c.End) {
			send(c, "overdue", ChangeOverdue, "")
		}
	}
	if changed {
		a.emit("changes", nil)
	}
}

func humanDuration(d time.Duration, lang string) string {
	zh := strings.HasPrefix(lang, "zh")
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		if zh {
			return fmt.Sprintf("%d 天", d/(24*time.Hour))
		}
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		if zh {
			return fmt.Sprintf("%d 小时", d/time.Hour)
		}
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	if zh {
		return fmt.Sprintf("%d 分钟", d/time.Minute)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

// shortDuration writes 2h, 1h30m or 45m rather than 2h0m0s.
func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
