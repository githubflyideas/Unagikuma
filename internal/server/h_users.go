package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

type userReq struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Role     string   `json:"role"`
	Areas    []string `json:"areas"`
	// Teams the user is a member of; nil leaves them as they are.
	Teams *[]string `json:"teams"`
	// Person, when creating, is the person in the user list who gets this
	// login; without it a new person is listed for the login.
	Person int64 `json:"person"`
}

// checkTeams validates the teams of a user request: with teams besides
// the built-in admin team they must be chosen; otherwise everyone is in
// the admin team.
func (q *userReq) checkTeams(cfg *config.Config, creating bool) ([]string, error) {
	if q.Teams == nil {
		listed := false // already a member in the configuration
		for _, t := range cfg.Teams {
			listed = listed || t.Has(q.Username)
		}
		if creating && cfg.MultiTeam() && !listed {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "choose the user's teams")
		}
		return nil, nil
	}
	teams := cleanList(*q.Teams)
	if len(teams) == 0 {
		if cfg.MultiTeam() {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "choose the user's teams")
		}
		teams = []string{config.AdminTeam}
	}
	for _, t := range teams {
		if cfg.FindTeam(t) == nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no team %s", t)
		}
	}
	return teams, nil
}

// putInTeams writes a user's team memberships, unless it is only the
// admin team they are in anyway.
func (s *Server) putInTeams(r *http.Request, user string, teams []string) error {
	cfg := s.app.EffectiveConfig()
	if teams == nil {
		return nil
	}
	explicit := false
	for _, t := range cfg.AllTeams() {
		explicit = explicit || t.Has(user)
	}
	if !explicit && len(teams) == 1 && teams[0] == config.AdminTeam {
		return nil
	}
	cur := strings.Join(cfg.TeamsOf(user), ",")
	if explicit && cur == strings.Join(teams, ",") {
		return nil
	}
	_, err := s.editConfig(r, "teams of "+user+": "+strings.Join(teams, ","), func(f *config.File) error {
		return setUserTeams(f, cfg, user, teams)
	})
	if err != nil {
		return s.configError(err)
	}
	return nil
}

// cleanAreas keeps sections only for operators: other roles see or change
// everything they are allowed to.
func (q *userReq) cleanAreas() []string {
	if q.Role != store.RoleOperator {
		return nil
	}
	var out []string
	for _, a := range q.Areas {
		if a = strings.TrimSpace(a); a != "" && a != "*" {
			out = append(out, a)
		}
	}
	return out
}

func validUsername(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func userError(err error) error {
	switch {
	case errors.Is(err, store.ErrUserExists):
		return Errorf(http.StatusConflict, CodeConflict, "%v", err)
	case errors.Is(err, store.ErrLastAdmin):
		return Errorf(http.StatusConflict, CodeConflict, "%v", err)
	case errors.Is(err, store.ErrNotFound):
		return notFound("user")
	}
	return Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
}

func (s *Server) usersList(w http.ResponseWriter, r *http.Request) (any, error) {
	list, err := s.app.Store().Users()
	if err != nil {
		return nil, err
	}
	type userJSON struct {
		store.User
		Teams []string `json:"teams"`
	}
	cfg := s.app.EffectiveConfig()
	out := make([]userJSON, 0, len(list))
	for _, u := range list {
		out = append(out, userJSON{u, cfg.TeamsOf(u.Username)})
	}
	return map[string]any{"items": out, "multi_team": cfg.MultiTeam()}, nil
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var q userReq
	if err := decodeJSON(r, s.maxBody(), &q); err != nil {
		return nil, err
	}
	if !validUsername(q.Username) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "invalid username")
	}
	if err := s.app.AllowAdd("users"); err != nil {
		return nil, quotaError(err)
	}
	teams, err := q.checkTeams(s.app.EffectiveConfig(), true)
	if err != nil {
		return nil, err
	}
	if q.Person != 0 {
		if _, err := s.app.Store().UserID(q.Username); err == nil {
			return nil, userError(store.ErrUserExists)
		}
		if err := s.app.Store().SetPersonLogin(q.Person, q.Username); err != nil {
			return nil, notFound("person")
		}
	}
	id, err := s.app.Store().CreateUserRole(q.Username, q.Password, q.Role, q.cleanAreas())
	if err != nil {
		if q.Person != 0 {
			_ = s.app.Store().SetPersonLogin(q.Person, "")
		}
		return nil, userError(err)
	}
	if err := s.putInTeams(r, q.Username, teams); err != nil {
		_ = s.app.Store().DeleteUser(id)
		return nil, err
	}
	if _, err := s.app.Store().LinkUsers(); err == nil {
		s.app.DirectoryChanged()
	}
	s.log.Info("user created", "user", q.Username, "role", q.Role, "by", authOf(r).user)
	auditOf(r).note("role", q.Role).note("areas", q.cleanAreas()).note("teams", teams).target = q.Username
	return map[string]any{"id": id}, nil
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, notFound("user")
	}
	var q userReq
	if err := decodeJSON(r, s.maxBody(), &q); err != nil {
		return nil, err
	}
	st := s.app.Store()
	before, found := s.userByID(id)
	if !found {
		return nil, notFound("user")
	}
	teams, err := q.checkTeams(s.app.EffectiveConfig(), false)
	if err != nil {
		return nil, err
	}
	a := auditOf(r)
	a.target = before.Username
	if teams != nil {
		a.note("teams", teams)
		if err := s.putInTeams(r, before.Username, teams); err != nil {
			return nil, err
		}
	}
	if q.Role != "" {
		a.note("before", map[string]any{"role": before.Role, "areas": before.Areas}).note("after", map[string]any{"role": q.Role, "areas": q.cleanAreas()})
		if err := st.SetRole(id, q.Role, q.cleanAreas()); err != nil {
			return nil, userError(err)
		}
	}
	if q.Password != "" {
		if err := st.SetPassword(id, q.Password); err != nil {
			return nil, userError(err)
		}
	}
	if q.Password != "" {
		a.note("password", "reset")
	}
	s.log.Info("user changed", "id", id, "role", q.Role, "password", q.Password != "", "by", authOf(r).user)
	return map[string]any{"ok": true}, nil
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, notFound("user")
	}
	if id == authOf(r).userID {
		return nil, Errorf(http.StatusConflict, CodeConflict, "you cannot delete your own account")
	}
	before, _ := s.userByID(id)
	if err := s.app.Store().DeleteUser(id); err != nil {
		return nil, userError(err)
	}
	auditOf(r).note("role", before.Role).note("areas", before.Areas).target = before.Username
	// The person stays, without the login: their addresses and history
	// remain for notifications.
	if err := s.app.Store().UnlinkUser(before.Username); err == nil {
		s.app.DirectoryChanged()
	}
	// Take them off their teams; a failure here (a replica) leaves a
	// member name the teams page lists as unknown.
	cfg := s.app.EffectiveConfig()
	for _, t := range cfg.Teams {
		if t.Has(before.Username) {
			_, _ = s.editConfig(r, "remove "+before.Username+" from teams", func(f *config.File) error {
				return setUserTeams(f, cfg, before.Username, nil)
			})
			break
		}
	}
	return map[string]any{"ok": true}, nil
}

// userByID finds an account for the audit log ("" when unknown).
func (s *Server) userByID(id int64) (store.User, bool) {
	list, _ := s.app.Store().Users()
	for _, u := range list {
		if u.ID == id {
			return u, true
		}
	}
	return store.User{Username: strconv.FormatInt(id, 10)}, false
}
