package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/store"
)

const sessionTTL = 30 * 24 * time.Hour

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	n, err := s.app.Store().UserCount()
	if err != nil {
		return nil, err
	}
	return map[string]any{"needed": n == 0, "local": isLoopback(r)}, nil
}

type setupReq struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
	Lang     string `json:"lang"`
	Timezone string `json:"timezone"`
}

// setup creates the first account. It needs the one-time token printed in
// the log unless the request comes from this machine.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) (any, error) {
	var req setupReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if ok, wait := s.login.Allow(s.ip(r), s.now()); !ok {
		return nil, rateLimited(wait)
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if n, _ := s.app.Store().UserCount(); n > 0 || s.setupToken == "" {
		return nil, Errorf(http.StatusConflict, CodeConflict, "setup is already done")
	}
	if !isLoopback(r) && subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.setupToken)) != 1 {
		return nil, Errorf(http.StatusForbidden, CodeForbidden, "wrong setup token (see the server log)")
	}
	if req.Username == "" {
		req.Username = "admin"
	}
	id, err := s.app.Store().CreateUser(req.Username, req.Password)
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	s.setupToken = ""
	if req.Lang != "" {
		_ = s.app.SetSetting("message_lang", i18n.Normalize(req.Lang))
		prefs, _ := json.Marshal(map[string]any{"lang": i18n.Normalize(req.Lang), "timezone": req.Timezone})
		_ = s.app.Store().SetPrefs(id, string(prefs))
	}
	auditOf(r).actor = req.Username
	res, tag, err := s.startSession(w, id, req.Username)
	auditOf(r).session = tag
	return res, err
}

// startSession signs userID in; tag names the new session in the audit log.
func (s *Server) startSession(w http.ResponseWriter, userID int64, name string) (res any, tag string, err error) {
	sid, csrf, err := s.app.Store().CreateSession(userID, sessionTTL)
	if err != nil {
		return nil, "", err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.opts.SecureCookies, MaxAge: int(sessionTTL.Seconds())})
	return map[string]any{"user": name, "csrf": csrf}, sessionTag(sid), nil
}

func (s *Server) sessionGet(w http.ResponseWriter, r *http.Request) (any, error) {
	ai := authOf(r)
	role := ai.role
	if role == "" {
		role = "token"
	}
	areas := ai.areas
	if areas == nil {
		areas = []string{}
	}
	out := map[string]any{"user": ai.user, "csrf": ai.csrf, "role": role, "areas": areas, "scopes": ai.scopes,
		// Shown under the logo.
		"version": s.app.Version(), "registered": s.app.License().State().Registered,
		// One machine: no site to choose in the history.
		"cluster": s.app.ReplicaStatus().Replica || len(s.app.EffectiveConfig().Nodes) > 0}
	// A team member works within their teams: the UI shows them only what
	// those teams own.
	if ai.team != nil {
		out["teams"] = ai.team.roles
	}
	return out, nil
}

func (s *Server) sessionLogin(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if ok, wait := s.login.Allow(s.ip(r), s.now()); !ok {
		return nil, rateLimited(wait)
	}
	u, err := s.app.Store().Authenticate(req.Username, req.Password)
	if err != nil {
		s.log.Warn("failed login", "user", req.Username, "ip", s.ip(r))
		_ = s.app.Store().Audit(store.AuditEntry{Actor: req.Username, Action: "session.login_failed", Addr: s.ip(r), Detail: withClient(nil, s.client(r, nil))})
		return nil, Errorf(http.StatusUnauthorized, CodeUnauthorized, "wrong username or password")
	}
	res, tag, err := s.startSession(w, u.ID, u.Username)
	if err != nil {
		return nil, err
	}
	_ = s.app.Store().Audit(store.AuditEntry{Actor: u.Username, Action: "session.login", Addr: s.ip(r),
		Detail: withClient(nil, s.client(r, &authInfo{sessionTag: tag}))})
	return res, nil
}

func (s *Server) sessionLogout(w http.ResponseWriter, r *http.Request) (any, error) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.app.Store().DeleteSession(c.Value)
	}
	if ai := authOf(r); ai != nil {
		_ = s.app.Store().Audit(store.AuditEntry{Actor: ai.user, Action: "session.logout", Addr: s.ip(r), Detail: withClient(nil, s.client(r, ai))})
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return nil, nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	ai := authOf(r)
	// Everyone changes their own password, whatever their role; an API
	// token is not a person and has none.
	if !ai.session {
		return nil, Errorf(http.StatusForbidden, CodeForbidden, "sign in to change your password: an API token has none")
	}
	if _, err := s.app.Store().Authenticate(ai.user, req.Old); err != nil {
		return nil, Errorf(http.StatusForbidden, CodeForbidden, "the current password is wrong")
	}
	if err := s.app.Store().SetPassword(ai.userID, req.New); err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	res, _, err := s.startSession(w, ai.userID, ai.user)
	return res, err
}

func (s *Server) prefsGet(w http.ResponseWriter, r *http.Request) (any, error) {
	ai := authOf(r)
	se, err := s.sessionUser(r)
	if err != nil || se == "" {
		return map[string]any{"user": ai.user}, nil
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(se), &m)
	return m, nil
}

func (s *Server) sessionUser(r *http.Request) (string, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", err
	}
	se, err := s.app.Store().SessionByID(c.Value)
	if err != nil {
		return "", err
	}
	return se.User.Prefs, nil
}

func (s *Server) prefsPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var m map[string]any
	if err := decodeJSON(r, 64<<10, &m); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(m)
	if err := s.app.Store().SetPrefs(authOf(r).userID, string(b)); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) languages(w http.ResponseWriter, r *http.Request) (any, error) {
	return i18n.Langs(), nil
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	if len(lang) > 5 && lang[len(lang)-5:] == ".json" {
		lang = lang[:len(lang)-5]
	}
	b, ok := i18n.Catalog(lang)
	if !ok {
		writeError(w, notFound("language"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
