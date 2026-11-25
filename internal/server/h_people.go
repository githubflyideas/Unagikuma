package server

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/directory"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// The contact directory: who may receive alerts. Channels are checked
// against it; see the directory package.

type personJSON struct {
	store.Person
	Channels []string `json:"channels"` // channels that send to this person
	// Reach says whether the system can reach them directly through
	// Notify: "" when it can, else why not (no_channel, channel_gone,
	// no_address, left).
	Reach string `json:"reach,omitempty"`
}

// personalChannel is a channel that delivers to individuals, and the kind
// of address it needs.
type personalChannel struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Contact string `json:"contact"` // email | phone
}

func personalChannels(cfg *config.Config) []personalChannel {
	out := []personalChannel{}
	for _, ch := range cfg.Channels {
		if _, kind, ok := directory.AddressKey(ch.Kind); ok {
			out = append(out, personalChannel{ch.Name, ch.Kind, kind})
		}
	}
	return out
}

// reachOf says whether a person can be reached through their notify
// channels; see personJSON.Reach.
func reachOf(p store.Person, chans []personalChannel) string {
	if p.Left() {
		return "left"
	}
	if !p.Active() {
		return "inactive"
	}
	if len(p.Notify) == 0 {
		return "no_channel"
	}
	for _, n := range p.Notify {
		for _, c := range chans {
			if c.Name != n {
				continue
			}
			if c.Contact == store.ContactPhone && len(p.Phones) > 0 || c.Contact == store.ContactEmail && len(p.Emails) > 0 {
				return ""
			}
			return "no_address"
		}
	}
	return "channel_gone"
}

// checkNotify validates the notify channels of a person being saved: each
// must deliver to individuals and the person must have that kind of
// address; once such channels exist, at least one is required.
func checkNotify(req personReq, chans []personalChannel) error {
	if len(req.Notify) == 0 {
		if len(chans) > 0 {
			return Errorf(http.StatusBadRequest, CodeInvalid, "choose how to reach this person: one of the e-mail, SMS or voice channels")
		}
		return nil
	}
	has := map[string]bool{store.ContactEmail: false, store.ContactPhone: false}
	for _, e := range req.Emails {
		has[store.ContactEmail] = has[store.ContactEmail] || strings.TrimSpace(e) != ""
	}
	for _, p := range req.Phones {
		has[store.ContactPhone] = has[store.ContactPhone] || strings.TrimSpace(p) != ""
	}
	for _, n := range req.Notify {
		var found *personalChannel
		for i := range chans {
			if chans[i].Name == n {
				found = &chans[i]
			}
		}
		if found == nil {
			return Errorf(http.StatusBadRequest, CodeInvalid, "%s is not an e-mail, SMS or voice channel", n)
		}
		if !has[found.Contact] {
			what := map[string]string{store.ContactEmail: "an e-mail address", store.ContactPhone: "a phone number"}[found.Contact]
			return Errorf(http.StatusBadRequest, CodeInvalid, "channel %s needs %s for this person", n, what)
		}
	}
	return nil
}

func (s *Server) people(w http.ResponseWriter, r *http.Request) (any, error) {
	d := s.app.Directory()
	cfg := s.app.EffectiveConfig()
	used := map[int64][]string{}
	for _, ch := range cfg.Channels {
		key, kind, ok := directory.AddressKey(ch.Kind)
		if !ok {
			continue
		}
		seen := map[int64]bool{}
		for _, item := range directory.SplitList(ch.Opts[key]) {
			p := d.ByHandle(item)
			if !strings.HasPrefix(item, "@") {
				p = d.Owner(kind, item)
			}
			if p != nil && !seen[p.ID] {
				seen[p.ID] = true
				used[p.ID] = append(used[p.ID], ch.Name)
			}
		}
	}
	pc := personalChannels(cfg)
	out := []personJSON{}
	for _, p := range d.People() {
		chans := used[p.ID]
		if chans == nil {
			chans = []string{}
		}
		sort.Strings(chans)
		reach := reachOf(p, pc)
		if reach == "no_channel" && len(pc) == 0 {
			reach = "" // nothing to choose from yet: not their problem
		}
		out = append(out, personJSON{p, chans, reach})
	}
	bounces, err := s.app.Store().Bounces()
	if err != nil {
		return nil, err
	}
	return map[string]any{"people": out, "bounces": bounces, "unknown": s.app.UnknownAddresses(), "personal_channels": pc,
		"threshold": cfg.Tunables.Int("contacts.bounce-threshold")}, nil
}

type personReq struct {
	Handle   string   `json:"handle"`
	Name     string   `json:"name"`
	Note     string   `json:"note"`
	Username string   `json:"username"`
	Emails   []string `json:"emails"`
	Phones   []string `json:"phones"`
	Notify   []string `json:"notify"`
}

func (s *Server) personSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req personReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	var id int64
	if v := r.PathValue("id"); v != "" {
		var err error
		if id, err = strconv.ParseInt(v, 10, 64); err != nil {
			return nil, notFound("person")
		}
	}
	if req.Username != "" {
		if _, err := s.app.Store().UserID(req.Username); err != nil {
			return nil, Errorf(http.StatusBadRequest, CodeInvalid, "no login named %s", req.Username)
		}
	}
	if err := checkNotify(req, personalChannels(s.app.EffectiveConfig())); err != nil {
		return nil, err
	}
	id, err := s.app.Store().SavePerson(store.Person{ID: id, Handle: req.Handle, Name: req.Name, Note: req.Note, Username: req.Username,
		Emails: req.Emails, Phones: req.Phones, Notify: req.Notify})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("person")
	case err != nil:
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "%v", err)
	}
	auditOf(r).target = req.Handle
	s.app.DirectoryChanged()
	return map[string]any{"id": id}, nil
}

func personID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, notFound("person")
	}
	return id, nil
}

func (s *Server) personDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := personID(r)
	if err != nil {
		return nil, err
	}
	// A login's own entry goes with the login, not before it.
	if people, err := s.app.Store().People(); err == nil {
		for _, p := range people {
			if p.ID == id && p.Username != "" {
				if _, err := s.app.Store().UserID(p.Username); err == nil {
					return nil, Errorf(http.StatusConflict, CodeConflict, "%s signs in as %s: delete the login first", p.Name, p.Username)
				}
			}
		}
	}
	if err := s.app.Store().DeletePerson(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, notFound("person")
		}
		return nil, err
	}
	s.app.DirectoryChanged()
	return nil, nil
}

// personLeft marks someone as having left: nothing is sent to them any
// more, and their login (if linked) is disabled.
func (s *Server) personLeft(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := personID(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		Left bool `json:"left"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	switch err := s.app.Store().SetLeft(id, req.Left); {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("person")
	case errors.Is(err, store.ErrLastAdmin):
		return nil, Errorf(http.StatusConflict, CodeConflict, "%v", err)
	case err != nil:
		return nil, err
	}
	auditOf(r).note("left", strconv.FormatBool(req.Left))
	s.app.DirectoryChanged()
	return nil, nil
}

// personStatus sets a contact's status: active, leave (on leave),
// transferred or left. Only active contacts are sent to; leaving also
// disables their login.
func (s *Server) personStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := personID(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if !store.ValidStatus(req.Status) {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "status is active, leave, transferred or left")
	}
	switch err := s.app.Store().SetStatus(id, req.Status); {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("person")
	case errors.Is(err, store.ErrLastAdmin):
		return nil, Errorf(http.StatusConflict, CodeConflict, "%v", err)
	case err != nil:
		return nil, err
	}
	auditOf(r).note("status", req.Status)
	s.app.DirectoryChanged()
	return nil, nil
}

// personReport switches the daily status report on or off for a person:
// it is no longer sent to their addresses, whichever channel lists them.
func (s *Server) personReport(w http.ResponseWriter, r *http.Request) (any, error) {
	id, err := personID(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		Receive bool `json:"receive"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	switch err := s.app.Store().SetReport(id, req.Receive); {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("person")
	case err != nil:
		return nil, err
	}
	auditOf(r).note("daily_report", req.Receive)
	s.app.DirectoryChanged()
	return nil, nil
}

// peopleImport adds a person for every channel address the directory
// does not know yet (see app.ImportContacts).
func (s *Server) peopleImport(w http.ResponseWriter, r *http.Request) (any, error) {
	created, errs := s.app.ImportContacts()
	auditOf(r).note("created", strconv.Itoa(created))
	s.app.DirectoryChanged()
	if errs == nil {
		errs = []string{}
	}
	return map[string]any{"created": created, "errors": errs}, nil
}

// peopleLookup checks a recipient list before a channel is saved: which
// addresses the directory does not know, and what else is wrong.
func (s *Server) peopleLookup(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Kind string `json:"kind"` // channel kind
		List string `json:"list"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	_, kind, ok := directory.AddressKey(req.Kind)
	if !ok {
		return map[string]any{"unknown": []string{}, "issues": []directory.Issue{}}, nil
	}
	d := s.app.Directory()
	_, all := d.Resolve(kind, req.List)
	unknown := d.Unknown(kind, req.List)
	if unknown == nil {
		unknown = []string{}
	}
	// issues are about addresses; that nobody at all is left to send to
	// is about the whole list, and has a field of its own.
	issues := []directory.Issue{}
	none := false
	for _, is := range all {
		if is.Code == directory.IssueNoRecipients {
			none = true
			continue
		}
		issues = append(issues, is)
	}
	return map[string]any{"kind": kind, "unknown": unknown, "issues": issues, "no_recipients": none}, nil
}

func (s *Server) bounceClear(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query()
	kind, addr := q.Get("kind"), q.Get("address")
	if (kind != store.ContactEmail && kind != store.ContactPhone) || addr == "" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "kind (email or phone) and address are required")
	}
	if err := s.app.Store().ClearBounces(kind, addr); err != nil {
		return nil, err
	}
	s.app.DirectoryChanged()
	return nil, nil
}

// chainIssues lists the channels of a chain, group members included,
// that have directory issues.
func chainIssues(cfg *config.Config, chain []string, issues map[string][]directory.Issue) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(names []string)
	walk = func(names []string) {
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			if len(issues[n]) > 0 {
				out = append(out, n)
			}
			if c := cfg.FindChannel(n); c != nil {
				walk(c.Members)
			}
		}
	}
	walk(chain)
	return out
}
