package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// auditRec collects what a handler wants recorded beyond the request
// itself; the wrapper writes it once the change succeeded.
type auditRec struct {
	actor   string // when nobody is logged in yet (first setup)
	session string // the sign-in the request itself started (first setup)
	target  string
	area    string
	detail  map[string]any
	skip    bool
}

type auditKey struct{}

func auditOf(r *http.Request) *auditRec {
	a, _ := r.Context().Value(auditKey{}).(*auditRec)
	if a == nil {
		return &auditRec{} // not audited: writes go nowhere
	}
	return a
}

// note adds detail to the audit entry of this request.
func (a *auditRec) note(k string, v any) *auditRec {
	if s, ok := v.([]string); ok && len(s) == 0 {
		return a // e.g. no sections: nothing to say
	}
	if a.detail == nil {
		a.detail = map[string]any{}
	}
	a.detail[k] = v
	return a
}

// noAudit lists changes that are tests or previews, not changes.
var noAudit = map[string]bool{
	"POST /api/v1/probe": true, "POST /api/v1/monitors/{name}/check": true, "POST /api/v1/channels/test": true,
	"POST /api/v1/policies/{name}/test": true, "POST /api/v1/settings/heartbeat/test": true,
	"POST /api/v1/quickadd/parse": true, "POST /api/v1/config/validate": true, "POST /api/v1/session": true,
	"DELETE /api/v1/session": true,
}

var singular = map[string]string{
	"monitors": "monitor", "incidents": "incident", "channels": "channel", "policies": "policy", "users": "user",
	"tokens": "token", "secrets": "secret", "nodes": "node",
}

// auditAction names a route: POST /api/v1/users → user.create,
// POST /api/v1/monitors/{name}/pause → monitor.pause.
func auditAction(pattern string) string {
	method, path, _ := strings.Cut(pattern, " ")
	segs := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	res, ok := singular[segs[0]]
	if !ok {
		res = segs[0]
	}
	var verb string
	var lits []string
	for _, s := range segs[1:] {
		if !strings.HasPrefix(s, "{") {
			lits = append(lits, s)
		}
	}
	switch {
	case len(lits) > 0:
		verb = strings.Join(lits, ".")
		if method == http.MethodDelete {
			verb += ".delete"
		}
	case method == http.MethodPost:
		verb = "create"
	case method == http.MethodPut:
		verb = "update"
	case method == http.MethodDelete:
		verb = "delete"
	default:
		verb = strings.ToLower(method)
	}
	return res + "." + verb
}

// pathTarget is the object a route acts on: its first path value.
func pathTarget(pattern string, r *http.Request) string {
	if i := strings.Index(pattern, "{"); i >= 0 {
		if j := strings.Index(pattern[i:], "}"); j > 0 {
			return r.PathValue(strings.TrimSuffix(pattern[i+1:i+j], "..."))
		}
	}
	return ""
}

// sessionTag names one sign-in in the audit log without revealing the
// session's secret: under an account several people share, changes from
// different browsers carry different tags.
func sessionTag(sid string) string {
	h := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(h[:4])
}

// client says which sign-in and which browser or program a request came
// from, for the audit log: an address alone cannot tell apart the people
// behind one office's NAT.
func (s *Server) client(r *http.Request, ai *authInfo) map[string]any {
	c := map[string]any{}
	if ai != nil && ai.sessionTag != "" {
		c["session"] = ai.sessionTag
	}
	if ua := []rune(r.UserAgent()); len(ua) > 0 {
		c["ua"] = string(ua[:min(len(ua), 256)])
	}
	if len(c) == 0 {
		return nil
	}
	return c
}

// editConfig applies a structured configuration change for this request;
// its config.apply audit entry carries the request's client.
func (s *Server) editConfig(r *http.Request, message string, change func(f *config.File) error) (app.ConfigState, error) {
	ai := authOf(r)
	return s.app.EditConfigFrom(ai.user, message, s.client(r, ai), change)
}

// withClient adds the client to an entry's detail.
func withClient(d map[string]any, c map[string]any) map[string]any {
	if c == nil {
		return d
	}
	if d == nil {
		d = map[string]any{}
	}
	d["client"] = c
	return d
}

// writeAudit records a successful change.
func (s *Server) writeAudit(pattern string, r *http.Request, ai *authInfo, a *auditRec, v any) {
	if a.skip {
		return
	}
	e := store.AuditEntry{Action: auditAction(pattern), Target: a.target, Area: a.area, Detail: a.detail, Addr: s.ip(r)}
	if ai != nil {
		e.Actor = ai.user
	} else {
		e.Actor = a.actor
	}
	if e.Target == "" {
		e.Target = pathTarget(pattern, r)
	}
	if e.Area == "" && strings.Contains(pattern, "/monitors/{name}") {
		if m := s.app.EffectiveConfig().FindMonitor(e.Target); m != nil {
			e.Area = m.Area
		}
	}
	if m, ok := v.(map[string]any); ok {
		if serial, ok := m["serial"]; ok {
			if e.Detail == nil {
				e.Detail = map[string]any{}
			}
			e.Detail["serial"] = serial
		}
	}
	who := ai
	if who == nil && a.session != "" {
		who = &authInfo{sessionTag: a.session}
	}
	e.Detail = withClient(e.Detail, s.client(r, who))
	if err := s.app.Store().Audit(e); err != nil {
		s.log.Error("audit", "err", err)
	}
}

// audited wraps a request so writeAudit can run after it.
func withAudit(r *http.Request) (*http.Request, *auditRec) {
	a := &auditRec{}
	return r.WithContext(context.WithValue(r.Context(), auditKey{}, a)), a
}

// auditLog lists the audit log for administrators.
func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) (any, error) {
	sites, _ := s.app.Store().MergedSites()
	if site := r.URL.Query().Get("site"); site != "" {
		// Another site's own log, as merged after a split.
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := s.app.Store().SiteAudit(store.MergeScope(site, r.URL.Query().Get("epoch")), limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": items, "sites": sites}, nil
	}
	aq := s.auditQuery(r)
	items, err := s.app.Store().AuditLog(aq)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "sites": sites}, nil
}

func (s *Server) auditQuery(r *http.Request) store.AuditQuery {
	q := r.URL.Query()
	aq := store.AuditQuery{Actor: q.Get("actor"), Action: q.Get("action"), Target: q.Get("target"), Area: q.Get("area"),
		Addr: q.Get("addr"), Session: q.Get("session"), Risk: q.Get("risk")}
	aq.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	aq.Limit, _ = strconv.Atoi(q.Get("limit"))
	if _, d := rangeOf(r); q.Get("range") != "" {
		aq.From = s.now().Add(-d)
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			aq.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			aq.To = t
		}
	}
	return aq
}

// auditExport downloads the filtered log as CSV or JSON (up to 10000
// entries, newest first). The download itself is recorded.
func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) (any, error) {
	aq := s.auditQuery(r)
	aq.Export, aq.Limit = true, 0
	items, err := s.app.Store().AuditLog(aq)
	if err != nil {
		return nil, err
	}
	format := r.URL.Query().Get("format")
	if format != "json" {
		format = "csv"
	}
	actor, ai := "", authOf(r)
	if ai != nil {
		actor = ai.user
	}
	_ = s.app.Store().Audit(store.AuditEntry{Actor: actor, Action: "audit.export", Addr: s.ip(r),
		Detail: withClient(map[string]any{"format": format, "entries": len(items)}, s.client(r, ai))})
	ct := map[string]string{"csv": "text/csv; charset=utf-8", "json": "application/json"}[format]
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="unagikuma-audit-%s.%s"`, s.now().Format("20060102-150405"), format))
	if err := store.WriteAudit(w, format, items); err != nil {
		s.log.Error("audit export", "err", err)
	}
	return nil, errHandled
}

// auditVerify checks the hash chain.
func (s *Server) auditVerify(w http.ResponseWriter, r *http.Request) (any, error) {
	if site := r.URL.Query().Get("site"); site != "" {
		n, err := s.app.Store().VerifySiteAudit(store.MergeScope(site, r.URL.Query().Get("epoch")))
		res := map[string]any{"ok": err == nil, "entries": n, "head": ""}
		if err != nil {
			res["problem"] = err.Error()
		}
		return res, nil
	}
	c, err := s.app.Store().VerifyAudit()
	if err != nil {
		return nil, err
	}
	if h := r.URL.Query().Get("head"); h != "" && c.OK && !s.app.Store().AuditContains(h) {
		c.OK, c.Problem = false, "the entry noted earlier is gone: entries were removed from the end"
	}
	return c, nil
}
