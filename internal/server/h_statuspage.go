package server

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/config"
	"github.com/githubflyideas/unagikuma/internal/notify"
	"github.com/githubflyideas/unagikuma/internal/store"
)

// ---- managing announcements ----

func (s *Server) statusPages(w http.ResponseWriter, r *http.Request) (any, error) {
	type pageJSON struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		Domain      string `json:"domain,omitempty"`
		Subscribe   string `json:"subscribe,omitempty"`
		Monitors    int    `json:"monitors"`
		Subscribers int    `json:"subscribers"`
		Pending     int    `json:"pending"`
	}
	counts, err := s.app.Store().SubscriberCounts()
	if err != nil {
		return nil, err
	}
	out := []pageJSON{}
	for _, sp := range s.app.EffectiveConfig().StatusPages {
		c := counts[sp.Name]
		out = append(out, pageJSON{sp.Name, sp.Title, s.pageURL(sp), sp.Domain, sp.Subscribe, len(s.published(sp)), c[0], c[1]})
	}
	return out, nil
}

func (s *Server) announcements(w http.ResponseWriter, r *http.Request) (any, error) {
	sp := s.statusPage(r.PathValue("name"))
	if sp == nil {
		return nil, notFound("status page")
	}
	return s.app.Store().Announcements(sp.Name, s.now().Add(-90*24*time.Hour), 200)
}

type announcementReq struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Resolved bool   `json:"resolved"`
	Notify   bool   `json:"notify"` // e-mail the page's subscribers
}

func (req *announcementReq) check() error {
	req.Title, req.Body = strings.TrimSpace(req.Title), strings.TrimSpace(req.Body)
	if req.Title == "" || len([]rune(req.Title)) > 200 || len(req.Body) > 20000 {
		return Errorf(http.StatusBadRequest, CodeInvalid, "an announcement needs a title (at most 200 characters) and a body of at most 20000")
	}
	if req.Severity == "" {
		req.Severity = "info"
	}
	if !slices.Contains(store.AnnouncementSeverities, req.Severity) {
		return Errorf(http.StatusBadRequest, CodeInvalid, "severity must be one of %s", strings.Join(store.AnnouncementSeverities, ", "))
	}
	return nil
}

func (s *Server) announcementCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	sp := s.statusPage(r.PathValue("name"))
	if sp == nil {
		return nil, notFound("status page")
	}
	var req announcementReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if err := req.check(); err != nil {
		return nil, err
	}
	a, err := s.app.Store().SaveAnnouncement(store.Announcement{Page: sp.Name, Title: req.Title, Body: req.Body,
		Severity: req.Severity, Author: authOf(r).user}, req.Resolved)
	if err != nil {
		return nil, err
	}
	s.announced(sp, a, req.Notify)
	auditOf(r).target = sp.Name
	return a, nil
}

func (s *Server) announcementUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	prev, err := s.app.Store().Announcement(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("announcement")
	} else if err != nil {
		return nil, err
	}
	var req announcementReq
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if err := req.check(); err != nil {
		return nil, err
	}
	prev.Title, prev.Body, prev.Severity = req.Title, req.Body, req.Severity
	a, err := s.app.Store().SaveAnnouncement(prev, req.Resolved)
	if err != nil {
		return nil, err
	}
	if sp := s.statusPage(a.Page); sp != nil {
		s.announced(sp, a, req.Notify)
	}
	auditOf(r).target = a.Page
	return a, nil
}

func (s *Server) announcementDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.app.Store().Announcement(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("announcement")
	} else if err != nil {
		return nil, err
	}
	if err := s.app.Store().DeleteAnnouncement(id); err != nil {
		return nil, err
	}
	s.pageCache.drop(a.Page)
	auditOf(r).target = a.Page
	return map[string]any{"ok": true}, nil
}

// announced refreshes the public page and, when asked, e-mails the
// subscribers in the background.
func (s *Server) announced(sp *config.StatusPage, a store.Announcement, notifySubs bool) {
	s.pageCache.drop(sp.Name)
	if !notifySubs || sp.Subscribe == "" {
		return
	}
	subs, err := s.app.Store().Subscribers(sp.Name)
	if err != nil || len(subs) == 0 {
		return
	}
	ch, err := s.subscribeChannel(sp)
	if err != nil {
		s.log.Warn("status page subscriptions", "page", sp.Name, "err", err)
		return
	}
	title := "[" + sp.Title + "] " + a.Title
	if a.ResolvedAt != nil {
		title = "[" + sp.Title + "] ✓ " + a.Title
	}
	link := s.pageURL(sp)
	go func() {
		for _, sub := range subs {
			body := a.Body + "\n\n" + link + "\n\n" + "Unsubscribe: " + link + "?unsubscribe=" + url.QueryEscape(sub.Token)
			s.mail(ch, sub.Email, title, body)
		}
	}()
}

// subscribeChannel is the page's e-mail channel with its secrets resolved.
func (s *Server) subscribeChannel(sp *config.StatusPage) (notify.Channel, error) {
	c := s.app.EffectiveConfig().FindChannel(sp.Subscribe)
	if c == nil || c.Kind != "email" {
		return notify.Channel{}, fmt.Errorf("subscribe=%s is not an email channel", sp.Subscribe)
	}
	ch := notify.Channel{Name: c.Name, Kind: c.Kind, Opts: map[string]string{}}
	resolve := s.app.Resolver()
	for k, v := range c.Opts {
		rv, err := resolve(v)
		if err != nil {
			return ch, err
		}
		ch.Opts[k] = rv
	}
	return ch, nil
}

func (s *Server) mail(ch notify.Channel, to, title, body string) {
	c := ch
	c.Opts = map[string]string{}
	for k, v := range ch.Opts {
		c.Opts[k] = v
	}
	c.Opts["to"] = to
	ctx, cancel := context.WithTimeout(context.Background(), s.app.EffectiveConfig().Tunables.Duration("notify.channel-timeout"))
	defer cancel()
	m := &notify.Message{Kind: "STATUS", Title: title, Body: body, Time: s.now()}
	if a := s.app.SendDirect(ctx, c, m); !a.OK {
		s.log.Warn("status page e-mail", "channel", ch.Name, "err", a.Error)
	}
}

// pageURL is where a status page is read.
func (s *Server) pageURL(sp *config.StatusPage) string {
	if sp.Domain != "" {
		return "https://" + sp.Domain + "/"
	}
	return strings.TrimSuffix(s.app.BaseURL(), "/") + sp.Path
}

// ---- public: subscriptions and feed ----

func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	sp := s.pageFor(r)
	if sp == nil || sp.Subscribe == "" {
		writeError(w, notFound("status page subscriptions"))
		return
	}
	if ok, wait := s.subs.Allow(s.ip(r), s.now()); !ok {
		writeError(w, rateLimited(wait))
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, 4096, &req); err != nil {
		writeError(w, err)
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(req.Email))
	if err != nil || addr.Name != "" || len(addr.Address) > 254 || !strings.Contains(addr.Address[strings.LastIndex(addr.Address, "@")+1:], ".") {
		writeError(w, Errorf(http.StatusBadRequest, CodeInvalid, "enter an e-mail address"))
		return
	}
	sub, err := s.app.Store().Subscribe(sp.Name, addr.Address, s.app.EffectiveConfig().Tunables.Int("statuspage.max-pending"))
	if errors.Is(err, store.ErrTooManyPending) {
		writeError(w, Errorf(http.StatusServiceUnavailable, CodeInvalid, "too many subscriptions are waiting for confirmation; try again later"))
		return
	} else if err != nil {
		writeError(w, err)
		return
	}
	// The same answer whether or not the address was subscribed already.
	if !sub.Confirmed {
		ch, err := s.subscribeChannel(sp)
		if err != nil {
			writeError(w, err)
			return
		}
		link := s.pageURL(sp) + "?confirm=" + url.QueryEscape(sub.Token)
		go s.mail(ch, sub.Email, "["+sp.Title+"] Confirm your subscription",
			"Open this link to receive "+sp.Title+" announcements by e-mail:\n\n"+link+"\n\nIf you did not ask for this, ignore this message.")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) subscription(w http.ResponseWriter, r *http.Request) {
	sp := s.pageFor(r)
	if sp == nil {
		writeError(w, notFound("status page"))
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, 4096, &req); err != nil {
		writeError(w, err)
		return
	}
	var err error
	if strings.HasSuffix(r.URL.Path, "/confirm") {
		err = s.app.Store().ConfirmSubscriber(sp.Name, req.Token)
	} else {
		err = s.app.Store().Unsubscribe(sp.Name, req.Token)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, notFound("subscription"))
		return
	} else if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Link    atomLink    `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
}

type atomEntry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Link    atomLink `xml:"link"`
	Content string   `xml:"content"`
}

// feed is the page's announcements as an Atom feed.
func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	sp := s.pageFor(r)
	if sp == nil {
		writeError(w, notFound("status page"))
		return
	}
	list, err := s.app.Store().Announcements(sp.Name, s.now().Add(-90*24*time.Hour), 50)
	if err != nil {
		writeError(w, err)
		return
	}
	link := s.pageURL(sp)
	f := atomFeed{Title: sp.Title, ID: link, Updated: s.now().UTC().Format(time.RFC3339), Link: atomLink{link}}
	for _, a := range list {
		title := a.Title
		if a.ResolvedAt != nil {
			title = "✓ " + title
		}
		f.Entries = append(f.Entries, atomEntry{Title: title, ID: link + "#a" + strconv.FormatInt(a.ID, 10),
			Updated: a.UpdatedAt.UTC().Format(time.RFC3339), Link: atomLink{link}, Content: a.Body})
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(f)
}

// ---- custom domains ----

// pageFor finds the page a public request is about: by name in the path,
// or by the host name it was sent to.
func (s *Server) pageFor(r *http.Request) *config.StatusPage {
	if name := r.PathValue("name"); name != "" && name != "-" {
		return s.statusPage(strings.TrimSuffix(name, ".json"))
	}
	return s.domainPage(r)
}

func (s *Server) domainPage(r *http.Request) *config.StatusPage {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, sp := range s.app.EffectiveConfig().StatusPages {
		if sp.Domain != "" && sp.Domain == host {
			return sp
		}
	}
	return nil
}

// domainOnly serves only the public status page on a status page's own
// host name: the console and its API stay on the main address.
func (s *Server) domainOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.domainPage(r) == nil {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		switch {
		case p == "/" || p == "/index.html":
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/status/-"
			next.ServeHTTP(w, r2)
		case p == "/feed.atom":
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/status/-/feed.atom"
			next.ServeHTTP(w, r2)
		case strings.HasPrefix(p, "/api/v1/status/-"), strings.HasPrefix(p, "/js/"), strings.HasPrefix(p, "/vendor/"),
			p == "/app.css", p == "/favicon.png", p == "/logo.png", strings.HasPrefix(p, "/badge/"):
			next.ServeHTTP(w, r)
		default:
			writeError(w, notFound("page"))
		}
	})
}

// drop forgets a cached page.
func (c *pageCache) drop(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

// statusPageCreate adds a STATUSPAGE record: a public page for the
// monitors its filter selects.
func (s *Server) statusPageCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Name   string `json:"name"`
		Title  string `json:"title"`
		Select string `json:"select"`
	}
	if err := decodeJSON(r, s.maxBody(), &req); err != nil {
		return nil, err
	}
	if !config.ValidName(req.Name) || strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Select) == "" {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "a status page needs a name, a title and which monitors it shows")
	}
	if _, err := config.ParseSelector(req.Select); err != nil {
		return nil, Errorf(http.StatusBadRequest, CodeInvalid, "select: %v", err)
	}
	if s.app.EffectiveConfig().HasName(req.Name) {
		return nil, Errorf(http.StatusConflict, CodeConflict, "%s already exists", req.Name)
	}
	spec := config.RecordSpec{Name: req.Name, Type: "STATUSPAGE", Target: strings.TrimSpace(req.Title), Opts: []config.Opt{{Key: "select", Value: req.Select}}}
	st, err := s.editConfig(r, "add status page "+req.Name, func(f *config.File) error { return f.UpsertRecord(spec) })
	if err != nil {
		return nil, s.configError(err)
	}
	auditOf(r).target = req.Name
	out := s.savedRecord(st, req.Name)
	if sp := s.statusPage(req.Name); sp != nil {
		out["url"] = s.pageURL(sp)
	}
	return out, nil
}
