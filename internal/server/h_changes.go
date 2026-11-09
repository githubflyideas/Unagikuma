package server

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Announced changes: planned work, who is told, the window it holds the
// affected monitors in maintenance, and the receipt when it ends.

type changeReq struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Impact      string    `json:"impact"`
	Select      string    `json:"select"`
	Policy      string    `json:"policy"`
	Channels    []string  `json:"channels"`
	StatusPage  string    `json:"status_page"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	// Notify sends the announcement (or the rescheduling notice) now;
	// on by default.
	Notify *bool `json:"notify"`
}

func (q changeReq) check(cfg *config.Config) error {
	if strings.TrimSpace(q.Title) == "" {
		return Errorf(http.StatusBadRequest, CodeInvalid, "a change needs a title")
	}
	if q.Start.IsZero() || !q.End.After(q.Start) {
		return Errorf(http.StatusBadRequest, CodeInvalid, "start and end are required, and the end must be after the start")
	}
	if q.Select != "" {
		if _, err := config.ParseSelector(q.Select); err != nil {
			return Errorf(http.StatusBadRequest, CodeInvalid, "select: %v", err)
		}
	}
	if q.Policy != "" && cfg.FindPolicy(q.Policy) == nil && q.Policy != "system" && q.Policy != "default" {
		return Errorf(http.StatusBadRequest, CodeInvalid, "unknown policy %s", q.Policy)
	}
	for _, c := range q.Channels {
		if cfg.FindChannel(c) == nil {
			return Errorf(http.StatusBadRequest, CodeInvalid, "unknown channel %s", c)
		}
	}
	if q.StatusPage != "" && !hasStatusPage(cfg, q.StatusPage) {
		return Errorf(http.StatusBadRequest, CodeInvalid, "unknown status page %s", q.StatusPage)
	}
	return nil
}

type changeJSON struct {
	store.Change
	Monitors int `json:"monitors"` // how many monitors the selector holds in maintenance
}

func (s *Server) changeView(c store.Change) changeJSON {
	n := 0
	if c.Select != "" {
		if sel, err := config.ParseSelector(c.Select); err == nil {
			for _, m := range s.app.EffectiveConfig().Monitors {
				if sel.Match(m) {
					n++
				}
			}
		}
	}
	return changeJSON{c, n}
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query()
	f := store.ChangeFilter{Query: q.Get("q"), State: q.Get("state"), Limit: qInt(r, "limit", 200)}
	for k, t := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(k); v != "" {
			d, err := time.ParseInLocation("2006-01-02", v, time.Local)
			if err != nil {
				return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%s must be YYYY-MM-DD", k)
			}
			if k == "to" {
				d = d.AddDate(0, 0, 1) // the whole day
			}
			*t = d
		}
	}
	list, err := s.app.Store().Changes(f)
	if err != nil {
		return nil, err
	}
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="changes.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "title", "state", "start", "end", "impact", "select", "created_by", "created_at", "finished_by", "finished_at", "note", "description"})
		ts := func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format(time.RFC3339)
		}
		for _, c := range list {
			_ = cw.Write([]string{strconv.FormatInt(c.ID, 10), c.Title, c.State, ts(c.Start), ts(c.End), c.Impact, c.Select, c.CreatedBy,
				ts(c.Created), c.FinishedBy, ts(c.Finished), c.Note, c.Description})
		}
		cw.Flush()
		return nil, errHandled
	}
	out := make([]changeJSON, 0, len(list))
	for _, c := range list {
		out = append(out, s.changeView(c))
	}
	return map[string]any{"items": out}, nil
}

func (s *Server) changeCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req changeReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	cfg := s.app.EffectiveConfig()
	if err := req.check(cfg); err != nil {
		return nil, err
	}
	by := authOf(r).user
	c, err := s.app.Store().SaveChange(store.Change{Title: req.Title, Description: req.Description, Impact: req.Impact, Select: req.Select,
		Policy: req.Policy, Channels: req.Channels, StatusPage: req.StatusPage, Start: req.Start, End: req.End, CreatedBy: by})
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	if req.StatusPage != "" {
		c = s.changeNotice(c, false)
	}
	if req.Notify == nil || *req.Notify {
		if _, err := s.app.Store().MarkChangeSent(c.ID, app.ChangeAnnounced); err == nil {
			if err := s.app.NotifyChange(c, app.ChangeAnnounced, by, ""); err != nil {
				return nil, Errorf(http.StatusBadRequest, CodeInvalid, "template: %v", err)
			}
		}
	}
	auditOf(r).target = c.Title
	s.app.ChangesChanged()
	return s.changeView(c), nil
}

// changeNotice creates or updates the change's status page notice;
// resolved closes it with the outcome.
func (s *Server) changeNotice(c store.Change, resolved bool) store.Change {
	sp := s.statusPage(c.StatusPage)
	if sp == nil {
		return c
	}
	title, body, err := s.app.RenderChange(c, app.ChangeAnnounced, c.CreatedBy, "")
	if resolved {
		event := map[string]string{store.ChangeDone: app.ChangeFinished, store.ChangeRolledBack: app.ChangeRolledBack, store.ChangeCancelled: app.ChangeCancelled}[c.State]
		title, body, err = s.app.RenderChange(c, event, c.FinishedBy, "")
	}
	if err != nil {
		return c
	}
	a := store.Announcement{ID: c.AnnouncementID, Page: sp.Name, Title: title, Body: body, Severity: "maintenance", Author: c.CreatedBy}
	if c.AnnouncementID != 0 {
		if prev, err := s.app.Store().Announcement(c.AnnouncementID); err == nil {
			a.CreatedAt, a.Page = prev.CreatedAt, prev.Page
		}
	}
	saved, err := s.app.Store().SaveAnnouncement(a, resolved)
	if err != nil {
		s.log.Warn("change notice on the status page", "change", c.ID, "err", err)
		return c
	}
	s.announced(sp, saved, false)
	if c.AnnouncementID != saved.ID {
		c.AnnouncementID = saved.ID
		if nc, err := s.app.Store().SaveChange(c); err == nil {
			c = nc
		}
	}
	return c
}

func changeID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, notFound("change")
	}
	return id, nil
}

func (s *Server) changeUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := changeID(r)
	if err != nil {
		return nil, err
	}
	prev, err := s.app.Store().Change(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("change")
	} else if err != nil {
		return nil, err
	}
	if !prev.Open() {
		return nil, Errorf(http.StatusConflict, CodeConflict, "the change is %s and can no longer be edited", prev.State)
	}
	var req changeReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if err := req.check(s.app.EffectiveConfig()); err != nil {
		return nil, err
	}
	moved := !req.Start.Equal(prev.Start) || !req.End.Equal(prev.End)
	c := prev
	c.Title, c.Description, c.Impact, c.Select, c.Policy, c.Channels, c.StatusPage = req.Title, req.Description, req.Impact, req.Select, req.Policy, req.Channels, req.StatusPage
	c.Start, c.End = req.Start, req.End
	if c, err = s.app.Store().SaveChange(c); err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	if moved {
		// Reminders count again from the new start.
		_ = s.app.Store().ResetChangeReminders(c.ID)
		if c.State == store.ChangeRunning && c.Start.After(s.now()) {
			c, _ = s.app.Store().SetChangeState(c.ID, store.ChangePlanned, "", "")
		}
		if req.Notify == nil || *req.Notify {
			if err := s.app.NotifyChange(c, app.ChangeRescheduled, authOf(r).user, ""); err != nil {
				return nil, Errorf(http.StatusBadRequest, CodeInvalid, "template: %v", err)
			}
		}
	}
	if c.StatusPage != "" {
		c = s.changeNotice(c, false)
	}
	auditOf(r).target = c.Title
	s.app.ChangesChanged()
	return s.changeView(c), nil
}

// changeFinish closes a change with its receipt: done, rolled back or
// cancelled, with a note. The maintenance window ends with it.
func (s *Server) changeFinish(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := changeID(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		State string `json:"state"`
		Note  string `json:"note"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	event := map[string]string{store.ChangeDone: app.ChangeFinished, store.ChangeRolledBack: app.ChangeRolledBack, store.ChangeCancelled: app.ChangeCancelled}[req.State]
	if event == "" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "state must be done, rolled_back or cancelled")
	}
	prev, err := s.app.Store().Change(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("change")
	} else if err != nil {
		return nil, err
	}
	if !prev.Open() {
		return nil, Errorf(http.StatusConflict, CodeConflict, "the change is already %s", prev.State)
	}
	if req.State == store.ChangeCancelled && prev.State == store.ChangeRunning {
		return nil, Errorf(http.StatusConflict, CodeConflict, "the change has started: mark it done or rolled back")
	}
	by := authOf(r).user
	c, err := s.app.Store().SetChangeState(id, req.State, req.Note, by)
	if err != nil {
		return nil, err
	}
	if ok, _ := s.app.Store().MarkChangeSent(c.ID, event); ok {
		if err := s.app.NotifyChange(c, event, by, ""); err != nil {
			s.log.Warn("change receipt", "change", c.ID, "err", err)
		}
	}
	if c.StatusPage != "" {
		c = s.changeNotice(c, true)
	}
	auditOf(r).note("state", req.State).target = c.Title
	s.app.ChangesChanged()
	return s.changeView(c), nil
}

func (s *Server) changeTemplates(w http.ResponseWriter, r *http.Request) (any, error) {
	tpls, custom := s.app.ChangeTemplates(s.app.MessageLang())
	return map[string]any{"events": app.ChangeEvents, "templates": tpls, "custom": custom}, nil
}

func (s *Server) changeTemplatesPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req map[string]app.ChangeTemplate
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if err := s.app.SetChangeTemplates(req); err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	return s.changeTemplates(w, r)
}

func hasStatusPage(cfg *config.Config, name string) bool {
	for _, sp := range cfg.StatusPages {
		if sp.Name == name {
			return true
		}
	}
	return false
}
