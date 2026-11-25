package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// Teams are TEAM records in monitors.conf: who works on which monitors,
// and where their notifications go. Keeping them in the file means they
// reach replicas with the rest of the configuration and every change is in
// its history. The built-in admin team needs no record: it owns every
// monitor without team= and cannot be deleted or renamed.

type teamJSON struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Members     []string `json:"members"`
	Channels    []string `json:"channels"`
	Mode        string   `json:"mode"`
	Builtin     bool     `json:"builtin,omitempty"`
	Monitors    int      `json:"monitors"`
	ETag        string   `json:"etag,omitempty"`
	// Unknown are member names with no user account (yet).
	Unknown []string `json:"unknown,omitempty"`
	// Unreachable are administrators in the team the system cannot tell
	// when its notifications fail: no contact linked to their login, or no
	// way to reach them chosen there.
	Unreachable []string `json:"unreachable,omitempty"`
	// IssueChannels are team channels whose recipients the contact
	// directory flags.
	IssueChannels []string `json:"issue_channels,omitempty"`
}

func (s *Server) teams(w http.ResponseWriter, r *http.Request) (any, error) {
	cfg := s.app.EffectiveConfig()
	users := map[string]string{}
	if list, err := s.app.Store().Users(); err == nil {
		for _, u := range list {
			users[u.Username] = u.Role
		}
	}
	etags := s.recordETags()
	issues := s.app.ChannelIssues()
	pc := personalChannels(cfg)
	reach := map[string]bool{}
	for _, p := range s.app.Directory().People() {
		if p.Username != "" && reachOf(p, pc) == "" {
			reach[p.Username] = true
		}
	}
	out := []teamJSON{}
	ts := teamOf(r)
	for _, t := range cfg.AllTeams() {
		if ts != nil && ts.roles[t.Name] == "" {
			continue // a team member sees their own teams only
		}
		mode := t.Mode
		if mode == "" {
			mode = "fallback"
		}
		tj := teamJSON{Name: t.Name, Title: t.Title, Description: t.Description, Members: nonNil(t.Members),
			Channels: nonNil(t.Channels), Mode: mode, Builtin: t.Name == config.AdminTeam, ETag: etags[t.Name],
			IssueChannels: chainIssues(cfg, t.Channels, issues)}
		for _, m := range cfg.Monitors {
			if m.TeamOf() == t.Name {
				tj.Monitors++
			}
		}
		for _, u := range t.Members {
			role, ok := users[u]
			if !ok {
				tj.Unknown = append(tj.Unknown, u)
			} else if role == store.RoleAdmin && !reach[u] {
				tj.Unreachable = append(tj.Unreachable, u)
			}
		}
		out = append(out, tj)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return out[i].Builtin
		}
		return out[i].Name < out[j].Name
	})
	return map[string]any{"items": out}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// teamSpec renders a team record, keeping options the forms do not edit.
func teamSpec(f *config.File, t config.Team) config.RecordSpec {
	var opts []config.Opt
	add := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			opts = append(opts, config.Opt{Key: k, Value: v})
		}
	}
	if t.Title != t.Name {
		add("title", t.Title)
	}
	add("members", strings.Join(cleanList(t.Members), ","))
	add("channels", strings.Join(cleanList(t.Channels), ","))
	if t.Mode == "all" {
		add("mode", "all")
	}
	add("description", t.Description)
	if rec, ok := f.Record(t.Name); ok {
		for _, o := range rec.Opts {
			switch o.Key {
			case "title", "members", "managers", "operators", "viewers", "channels", "mode", "description":
			default:
				opts = append(opts, o)
			}
		}
	}
	return config.RecordSpec{Name: t.Name, Type: "TEAM", Opts: opts}
}

func cleanList(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) teamSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name        string    `json:"name"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Members     *[]string `json:"members"`
		Channels    *[]string `json:"channels"`
		Mode        string    `json:"mode"`
		Message     string    `json:"message"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if !config.ValidName(req.Name) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "a team needs a valid name")
	}
	if req.Mode != "" && req.Mode != "fallback" && req.Mode != "all" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "mode is fallback or all")
	}
	t := config.Team{Name: req.Name, Title: req.Title, Description: req.Description, Mode: req.Mode}
	// Lists left out of the request keep what the team has.
	if cur := s.app.EffectiveConfig().FindTeam(req.Name); cur != nil {
		t.Members, t.Channels = cur.Members, cur.Channels
	}
	if req.Members != nil {
		t.Members = *req.Members
	}
	if req.Channels != nil {
		t.Channels = *req.Channels
	}
	st, err := s.editConfig(r, req.Message, ifMatch(r, req.Name, func(f *config.File) error {
		return f.UpsertRecord(teamSpec(f, t))
	}))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.savedRecord(st, req.Name), nil
}

func (s *Server) teamDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	name := r.PathValue("name")
	if name == config.AdminTeam {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "the admin team cannot be deleted")
	}
	cfg := s.app.EffectiveConfig()
	t := cfg.FindTeam(name)
	if t == nil || t.Implicit {
		return nil, notFound("team")
	}
	n := 0
	for _, m := range cfg.Monitors {
		if m.TeamOf() == name {
			n++
		}
	}
	if n > 0 {
		return nil, Errorf(http.StatusConflict, CodeConflict, "team %s still owns %d monitors: move them to another team first", name, n)
	}
	st, err := s.editConfig(r, "delete team "+name, ifMatch(r, name, func(f *config.File) error { return f.DeleteRecord(name) }))
	if err != nil {
		return nil, s.configError(err)
	}
	return s.saved(st), nil
}

// setUserTeams makes a user a member of exactly the given teams; the admin
// team gets a record when it needs one.
func setUserTeams(f *config.File, cfg *config.Config, user string, teams []string) error {
	want := map[string]bool{}
	for _, t := range teams {
		want[t] = true
	}
	for _, t := range cfg.AllTeams() {
		has := t.Has(user)
		if has == want[t.Name] {
			continue
		}
		next := *t
		next.Members = nil
		for _, m := range t.Members {
			if m != user {
				next.Members = append(next.Members, m)
			}
		}
		if want[t.Name] {
			next.Members = append(next.Members, user)
		}
		if err := f.UpsertRecord(teamSpec(f, next)); err != nil {
			return err
		}
	}
	return nil
}
