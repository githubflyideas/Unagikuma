package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/engine"
	"github.com/githubflyideas/unagikuma/internal/ratelimit"
)

// Options configure the server.
type Options struct {
	Log *slog.Logger
	// Web is the UI file system (index.html at its root); nil serves no UI.
	Web fs.FS
	// TrustedProxy reports peers whose X-Forwarded-For is honoured.
	TrustedProxy func(net.IP) bool
	// SecureCookies marks the session cookie Secure (behind TLS).
	SecureCookies bool
}

// Server is the HTTP layer.
// routeInfo is a registered API route (for the tests that walk them all).
type routeInfo struct{ pattern, scope string }

type Server struct {
	routeList []routeInfo
	app       *app.App
	opts      Options
	log       *slog.Logger
	hub       *Hub
	mux       *http.ServeMux
	now       func() time.Time
	api       *ratelimit.Keyed
	login     *ratelimit.Keyed
	joins     *ratelimit.Keyed // cluster requests not yet authenticated
	push      *ratelimit.Keyed
	subs      *ratelimit.Keyed // public status page subscriptions, per IP

	setupMu    sync.Mutex
	setupToken string

	pageCache pageCache
	etags     etagCache
	idem      idemCache

	// Live events are handed to a worker so the engine never waits for
	// the HTTP layer (enriching a state event reads back from the engine).
	pubq      chan pubEvent
	pubLost   atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
}

type pubEvent struct {
	typ string
	v   any
}

// authInfo is the authenticated caller.
type authInfo struct {
	user    string
	userID  int64
	csrf    string // session only
	scopes  map[string]bool
	session bool
	token   string
	// sessionTag names this sign-in (or token) in the audit log.
	sessionTag string
	role       string
	areas      []string // $SCOPE sections a limited operator may change
	// team is set for a member of one or more teams (not an administrator):
	// it sees and changes only what its teams own.
	team *teamScope
}

type ctxKey struct{}

func authOf(r *http.Request) *authInfo {
	a, _ := r.Context().Value(ctxKey{}).(*authInfo)
	return a
}

const sessionCookie = "unagikuma_session"

// New builds the server and starts publishing live events.
func New(a *app.App, o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	boot := make([]byte, 4)
	_, _ = rand.Read(boot)
	s := &Server{app: a, opts: o, log: o.Log, mux: http.NewServeMux(), now: time.Now}
	ts := a.EffectiveConfig().Tunables
	s.hub = NewHub(hex.EncodeToString(boot), 1024, func() int { return a.EffectiveConfig().Tunables.Int("api.max-sse") })
	rate := func(key string) (float64, int) {
		r := ts.Rate(key)
		return r, max(1, int(r*10))
	}
	ar, ab := rate("api.rate")
	lr, _ := rate("api.login-rate")
	pr, pb := rate("api.push-rate")
	s.api = ratelimit.NewKeyed(ar, ab, 0)
	s.login = ratelimit.NewKeyed(lr, 3, 0)
	jr, _ := rate("cluster.join-rate")
	s.joins = ratelimit.NewKeyed(jr, 10, 0)
	s.push = ratelimit.NewKeyed(pr, pb, 0)
	sr, _ := rate("statuspage.subscribe-rate")
	s.subs = ratelimit.NewKeyed(sr, 3, 0)
	s.pubq, s.done = make(chan pubEvent, 4096), make(chan struct{})
	a.SetPublisher(func(typ string, v any) {
		select {
		case s.pubq <- pubEvent{typ, v}:
		default:
			s.pubLost.Store(true)
		}
	})
	go s.publishLoop()
	s.routes()
	if n, _ := a.Store().UserCount(); n == 0 {
		s.setupToken = randomHex(16)
		s.log.Info("first run: open the web UI to finish setup", "setup_token", s.setupToken)
	}
	return s
}

// Close stops the live event worker.
func (s *Server) Close() { s.closeOnce.Do(func() { close(s.done) }) }

func (s *Server) publishLoop() {
	for {
		var ev pubEvent
		select {
		case <-s.done:
			return
		case ev = <-s.pubq:
		}
		if s.pubLost.Swap(false) {
			// Events were dropped: tell clients to refetch everything.
			s.hub.Publish("reset", map[string]any{})
		}
		monitor := ""
		switch v := ev.v.(type) {
		case map[string]any:
			if n, ok := v["name"].(string); ok {
				monitor = n
			}
			if n, ok := v["monitor"].(string); ok {
				monitor = n
			}
		case engine.Event:
			monitor = v.Monitor
		}
		if ev.typ == "state" {
			if m, ok := ev.v.(map[string]any); ok {
				if name, _ := m["name"].(string); name != "" {
					if row, ok := s.row(name); ok {
						ev.v = row
					}
				}
			}
		}
		s.hub.PublishFor(ev.typ, monitor, ev.v)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SetupToken returns the one-time setup token ("" once set up).
func (s *Server) SetupToken() string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupToken
}

// Handler returns the root handler with the common middleware.
func (s *Server) Handler() http.Handler {
	return securityHeaders(recoverer(s.log, s.domainOnly(s.mux)))
}

// apiFunc is a handler returning a value to encode or an error.
type apiFunc func(w http.ResponseWriter, r *http.Request) (any, error)

// handle registers an API endpoint with authentication and scope checks.
// scope "" means public; "read" also accepts any authenticated caller.
func (s *Server) handle(pattern, scope string, f apiFunc) {
	s.routeList = append(s.routeList, routeInfo{pattern, scope})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scope != "" {
			ai, err := s.authenticate(r)
			if err != nil {
				writeError(w, err)
				return
			}
			if !ai.scopes[scope] && !ai.scopes["admin"] {
				writeError(w, Errorf(http.StatusForbidden, CodeForbidden, "this token lacks the %q scope", scope))
				return
			}
			if ai.team != nil && !teamRoutes[pattern] {
				writeError(w, errTeamOnly)
				return
			}
			if ai.session && r.Method != http.MethodGet && r.Method != http.MethodHead {
				if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Unagikuma-CSRF")), []byte(ai.csrf)) != 1 {
					writeError(w, Errorf(http.StatusForbidden, CodeForbidden, "missing or wrong CSRF token"))
					return
				}
			}
			key := "u:" + ai.user
			if ai.token != "" {
				key = "t:" + ai.token
			}
			if ok, wait := s.api.Allow(key, s.now()); !ok {
				writeError(w, rateLimited(wait))
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ai))
		}
		if (scope == "write" || scope == "admin") && r.Method != "GET" && r.Method != "HEAD" && !s.app.ConfigAuthority() && sharedConfigRoute(r.URL.Path) {
			writeError(w, Errorf(http.StatusConflict, CodeConflict, "configuration is read-only on this site"))
			return
		}
		audit := r.Method != http.MethodGet && r.Method != http.MethodHead && scope != "read" && !noAudit[pattern]
		var rec *auditRec
		if audit {
			r, rec = withAudit(r)
		}
		respond := func(v any, err error) {
			if err == errHandled {
				return
			}
			if err != nil {
				writeError(w, err)
				return
			}
			if audit {
				s.writeAudit(pattern, r, authOf(r), rec, v)
			}
			if v == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, http.StatusOK, v)
		}
		if ai := authOf(r); ai != nil {
			caller := "u:" + ai.user
			if ai.token != "" {
				caller = "t:" + ai.token
			}
			if s.idempotent(w, r, caller, func(r *http.Request) (any, error) { return f(w, r) }, respond) {
				return
			}
		}
		respond(f(w, r))
	})
	s.mux.Handle(pattern, h)
}

var errUnauthorized = Errorf(http.StatusUnauthorized, CodeUnauthorized, "login required")

// authenticate resolves a session cookie or a bearer token.
func (s *Server) authenticate(r *http.Request) (*authInfo, error) {
	st := s.app.Store()
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		plain := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		tok, err := st.TokenByPlain(plain)
		if err != nil {
			return nil, errUnauthorized
		}
		ai := &authInfo{user: "token:" + tok.Name, userID: tok.UserID, scopes: map[string]bool{}, token: tok.Prefix, sessionTag: tok.Prefix}
		for _, sc := range tok.Scopes {
			ai.scopes[sc] = true
		}
		// write implies ack implies read.
		if ai.scopes["write"] {
			ai.scopes["ack"] = true
		}
		if ai.scopes["ack"] || ai.scopes["write"] {
			ai.scopes["read"] = true
		}
		return ai, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		if n, _ := st.UserCount(); n == 0 {
			return nil, Errorf(http.StatusUnauthorized, CodeSetupRequired, "setup required")
		}
		return nil, errUnauthorized
	}
	se, err := st.SessionByID(c.Value)
	if err != nil {
		return nil, errUnauthorized
	}
	ai := &authInfo{user: se.User.Username, userID: se.User.ID, csrf: se.CSRF, session: true, sessionTag: sessionTag(c.Value),
		scopes: roleScopes(se.User.Role), role: se.User.Role, areas: se.User.Areas}
	if ts := teamScopeFor(s.app.EffectiveConfig(), se.User.Username, se.User.Role); ts != nil {
		ai.team, ai.scopes = ts, ts.scopes()
	}
	return ai, nil
}

func (s *Server) ip(r *http.Request) string { return clientIP(r, s.opts.TrustedProxy) }

// isLoopback reports whether the request comes from this machine itself.
// A request relayed by a reverse proxy on this machine also arrives from
// loopback, so any forwarding header disqualifies it.
func isLoopback(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func qInt(r *http.Request, k string, def int) int {
	if v := r.URL.Query().Get(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func qInt64(r *http.Request, k string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(k), 10, 64)
	return n
}

func (s *Server) maxBody() int64 { return int64(s.app.EffectiveConfig().Tunables.Int("api.max-body")) }

func notFound(what string) error {
	return Errorf(http.StatusNotFound, CodeNotFound, "%s not found", what)
}

func sharedConfigRoute(path string) bool {
	// Pause and resume are site-local: allowed on every node, including a
	// read-only replica, and must not require the master's configuration.
	if strings.HasSuffix(path, "/pause") || strings.HasSuffix(path, "/bulk-pause") {
		return false
	}
	if strings.HasSuffix(path, "/test") || strings.HasSuffix(path, "/predict") || strings.HasSuffix(path, "/push-token") || strings.HasSuffix(path, "/push/rotate") {
		return false
	}
	for _, p := range []string{"/api/v1/config", "/api/v1/monitors", "/api/v1/channels", "/api/v1/policies", "/api/v1/teams", "/api/v1/people", "/api/v1/changes", "/api/v1/tunables", "/api/v1/maintenance", "/api/v1/nodes", "/api/v1/status-pages", "/api/v1/import"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
