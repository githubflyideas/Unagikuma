package server

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/engine"
)

type maintJSON struct {
	engine.MaintView
	Select   string `json:"select"`
	Schedule string `json:"schedule"` // the record's timing options as written
	Duration string `json:"duration"`
	ETag     string `json:"etag,omitempty"`
	Notify   bool   `json:"notify"`
	Line     int    `json:"line"`
	// The times are null when not known (a window that is not open has
	// no start), not "0001-01-01".
	Start   *time.Time `json:"start"`
	End     *time.Time `json:"end"`
	Next    *time.Time `json:"next"`
	NextEnd *time.Time `json:"next_end"`
}

func timeOrNull(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Server) maintList(w http.ResponseWriter, r *http.Request) (any, error) {
	cfg := s.app.EffectiveConfig()
	byName := map[string]*config.Maint{}
	for _, mt := range cfg.Maints {
		byName[mt.Name] = mt
	}
	out := []maintJSON{}
	for _, v := range s.app.Engine().Maintenance() {
		if mt := byName[v.Name]; teamOf(r) != nil && (mt == nil || !seesAll(r, mt.Monitors)) {
			continue // a team member sees the windows of their own monitors only
		}
		j := maintJSON{MaintView: v, Start: timeOrNull(v.Start), End: timeOrNull(v.End), Next: timeOrNull(v.Next), NextEnd: timeOrNull(v.NextEnd)}
		if mt := byName[v.Name]; mt != nil {
			j.Select, j.Notify, j.Line = mt.Select.String(), mt.Notify, mt.Pos.Line
			j.Duration = mt.Schedule.Duration.String()
			j.Schedule = scheduleText(s.app.Config().File, mt.Name)
			j.ETag = s.recordETags()[mt.Name]
		}
		out = append(out, j)
	}
	return map[string]any{"items": out}, nil
}

// scheduleText shows a window's timing options as written in the file.
func scheduleText(f *config.File, name string) string {
	r, ok := f.Record(name)
	if !ok {
		return ""
	}
	var parts []string
	for _, o := range r.Opts {
		switch o.Key {
		case "at", "every", "start", "cron":
			parts = append(parts, o.Key+"="+o.Value)
		}
	}
	return strings.Join(parts, " ")
}

func (s *Server) maintSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name     string `json:"name"`
		Title    string `json:"title"`
		Select   string `json:"select"`
		At       string `json:"at"`
		Every    string `json:"every"`
		Start    string `json:"start"`
		Cron     string `json:"cron"`
		Duration string `json:"duration"`
		Notify   bool   `json:"notify"`
		Message  string `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	cfg := s.app.EffectiveConfig()
	existing := false
	for _, mt := range cfg.Maints {
		existing = existing || mt.Name == req.Name
		if mt.Name == req.Name && !s.allowsMonitors(authOf(r), mt.Monitors) {
			return nil, errOutsideAreas
		}
	}
	if ai := authOf(r); ai.restricted() {
		sel, err := config.ParseSelector(req.Select)
		if err != nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "select: %v", err)
		}
		var names []string
		for _, m := range cfg.Monitors {
			if sel.Match(m) {
				names = append(names, m.Name)
			}
		}
		if len(names) == 0 || !s.allowsMonitors(ai, names) {
			return nil, errOutsideAreas
		}
	}
	if !existing && cfg.HasName(req.Name) {
		return nil, Errorf(http.StatusConflict, CodeConflict, "%s already exists", req.Name)
	}
	spec := config.RecordSpec{Name: req.Name, Type: "MAINT", Target: req.Title}
	for _, kv := range [][2]string{{"select", req.Select}, {"at", req.At}, {"every", req.Every}, {"start", req.Start}, {"cron", req.Cron}, {"duration", req.Duration}} {
		if kv[1] != "" {
			spec.Opts = append(spec.Opts, config.Opt{Key: kv[0], Value: kv[1]})
		}
	}
	if req.Notify {
		spec.Opts = append(spec.Opts, config.Opt{Key: "notify", Value: "yes"})
	}
	msg := req.Message
	if msg == "" {
		msg = "maintenance " + req.Name
	}
	st, err := s.editConfig(r, msg, ifMatch(r, spec.Name, func(f *config.File) error { return f.UpsertRecord(spec) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.savedRecord(st, spec.Name), nil
}

func (s *Server) maintDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	found := false
	for _, mt := range s.app.EffectiveConfig().Maints {
		if mt.Name == name {
			found = true
			if !s.allowsMonitors(authOf(r), mt.Monitors) {
				return nil, errOutsideAreas
			}
		}
	}
	if !found {
		return nil, notFound("maintenance window")
	}
	st, err := s.editConfig(r, "delete maintenance "+name, ifMatch(r, name, func(f *config.File) error { return f.DeleteRecord(name) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

// kumaImport previews (?apply=0) or applies an Uptime Kuma backup or a
// Nagios object configuration sent as the request body.
func (s *Server) kumaImport(w http.ResponseWriter, r *http.Request) (any, error) {
	if src := r.PathValue("source"); src != "kuma" && src != "nagios" && src != "checkmate" && src != "gatus" {
		return nil, notFound("import source")
	}
	limit := int64(32 << 20)
	if r.PathValue("source") == "gatus" || r.PathValue("source") == "checkmate" {
		limit = 1 << 20
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, Errorf(http.StatusRequestEntityTooLarge, CodeTooLarge, "the upload exceeds the source size limit (%d bytes)", limit)
	}
	q := r.URL.Query()
	o := app.KumaOptions{Area: strings.TrimSpace(q.Get("area")), Role: q.Get("role"), Manager: q.Get("manager"), Source: r.PathValue("source")}
	if o.Source == "kuma" {
		o.Source = ""
	}
	if q.Get("apply") != "1" {
		res, err := s.app.PreviewKuma(data, o)
		if err != nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
		}
		return res, nil
	}
	st, res, users, err := s.app.ImportKuma(data, authOf(r).user, o)
	if err != nil {
		if res == nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
		}
		return nil, s.configError(err)
	}
	if users == nil {
		users = []app.UserNote{}
	}
	a := auditOf(r).note("monitors", res.Monitors).note("channels", res.Channels).note("users", users).note("role", o.Role)
	a.target, a.area = o.Manager, o.Area
	out := s.saved(st)
	out["monitors"], out["channels"], out["notes"], out["users"] = res.Monitors, res.Channels, res.Notes, users
	return out, nil
}
