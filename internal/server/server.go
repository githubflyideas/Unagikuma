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
	"os"
	"path/filepath"
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
	// rawRoutes records the /api routes registered directly on the mux
	// (they authenticate themselves, or are public). routeList and
	// rawRoutes together are the whole API surface; openapi_test.go
	// checks the published spec against both.
	rawRoutes []routeInfo
	app       *app.App
	opts      Options
	log       *slog.Logger
	hub       *Hub
	mux       *http.ServeMux
	now       func() time.Time
	api       *ratelimit.Keyed
	login     *ratelimit.Keyed
	loginAll  *ratelimit.Keyed // every sign-in and setup together
	joins     *ratelimit.Keyed // cluster requests not yet authenticated
	push      *ratelimit.Keyed
	subs      *ratelimit.Keyed // public status page subscriptions, per IP

	setupMu    sync.Mutex
	setupToken string

	pageCache pageCache
	etags     etagCache
	uptimes   uptimeCache
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
	// group is set for an account limited to some monitor groups (not an
	// administrator): it sees and changes only the monitors in them.
	group  *groupScope
	groups []string
}

type ctxKey struct{}

func authOf(r *http.Request) *authInfo {
	a, _ := r.Context().Value(ctxKey{}).(*authInfo)
	return a
}

// sessionCookiePrefix is the stem of the session cookie. The installation
// number is appended so two instances on the same host (browsers do not
// isolate cookies by port) do not share a login.
const sessionCookiePrefix = "unagikuma_session"

func (s *Server) sessionCookie() string {
	id := s.app.License().InstallationID()
	if id == "" {
		return sessionCookiePrefix
	}
	return sessionCookiePrefix + "_" + id
}

// New builds the server and starts publishing live events.
func New(a *app.App, o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	boot := make([]byte, 4)
	_, _ = rand.Read(boot)
	s := &Server{app: a, opts: o, log: o.Log, mux: http.NewServeMux(), now: time.Now}
	s.hub = NewHub(hex.EncodeToString(boot), 1024, func() int { return a.EffectiveConfig().Tunables.Int("api.max-sse") })
	// All sign-ins together, whatever their address: with the bound on
	// concurrent password hashes this keeps a flood of failed logins
	// from many addresses to a short queue.
	s.loginAll = ratelimit.NewKeyed(loginAllRate, loginAllBurst, 0)
	// The other limits take their rates from the tunables in applyRates.
	s.api, s.login = ratelimit.NewKeyed(1, 1, 0), ratelimit.NewKeyed(1, 1, 0)
	s.joins, s.push, s.subs = ratelimit.NewKeyed(1, 1, 0), ratelimit.NewKeyed(1, 1, 0), ratelimit.NewKeyed(1, 1, 0)
	s.applyRates()
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
	s.prepareSetupToken()
	return s
}

// applyRates sets the request limits from the rate tunables ($SET
// api.rate and the like), at the start and after every configuration
// change, so a changed limit holds at once, as the tunables page shows.
func (s *Server) applyRates() {
	ts := s.app.EffectiveConfig().Tunables
	rate := func(key string) (float64, int) {
		r := ts.Rate(key)
		return r, max(1, int(r*10))
	}
	ar, ab := rate("api.rate")
	lr, _ := rate("api.login-rate")
	pr, pb := rate("api.push-rate")
	jr, _ := rate("cluster.join-rate")
	sr, _ := rate("statuspage.subscribe-rate")
	s.api.SetRate(ar, ab)
	s.login.SetRate(lr, 3)
	s.joins.SetRate(jr, 10)
	s.push.SetRate(pr, pb)
	s.subs.SetRate(sr, 3)
}

// setupTokenFile holds the one-time setup token in the data directory
// until the first account is made. The token is not written to the log,
// which is often kept and shared; the log names the file.
const setupTokenFile = "setup-token"

// prepareSetupToken makes the setup token on a first run and writes it,
// readable by the owner only, to setupTokenFile; otherwise it removes a
// file a finished setup left.
func (s *Server) prepareSetupToken() {
	path := filepath.Join(s.app.DataDir(), setupTokenFile)
	if n, _ := s.app.Store().UserCount(); n > 0 {
		_ = os.Remove(path)
		return
	}
	s.setupToken = randomHex(16)
	_ = os.Remove(path) // a file left by another user is not reused
	if err := os.WriteFile(path, []byte(s.setupToken+"\n"), 0o600); err != nil {
		s.log.Warn("first run: the setup token file cannot be written; only this machine can finish setup", "file", path, "err", err)
		return
	}
	s.log.Info("first run: open the web UI to finish setup; from another machine it asks for the setup token in this file", "file", path)
}

// SetupTokenFile is where the setup token is ("" once set up).
func (s *Server) SetupTokenFile() string {
	if s.SetupToken() == "" {
		return ""
	}
	return filepath.Join(s.app.DataDir(), setupTokenFile)
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
			s.applyRates()
		}
		if ev.typ == "serial" {
			s.applyRates()
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
	return securityHeaders(recoverer(s.log, s.domainOnly(s.methodGuard(s.mux))))
}

// apiFunc is a handler returning a value to encode or an error.
type apiFunc func(w http.ResponseWriter, r *http.Request) (any, error)

// loginAllRate and loginAllBurst bound sign-ins from all addresses
// together (per address it is the api.login-rate tunable).
const (
	loginAllRate  = 2.0 // per second
	loginAllBurst = 20
)

// allowLogin applies the per-address and the overall sign-in limits.
func (s *Server) allowLogin(r *http.Request) error {
	now := s.now()
	if ok, wait := s.login.Allow(s.ip(r), now); !ok {
		return rateLimited(wait)
	}
	if ok, wait := s.loginAll.Allow("all", now); !ok {
		return rateLimited(wait)
	}
	return nil
}

// scopeError refuses a request the caller's access does not cover, in
// the caller's terms: a signed-in account has a role, a token has scopes.
func scopeError(ai *authInfo, scope string) error {
	if ai.session {
		return Errorf(http.StatusForbidden, CodeForbidden, "your role (%s) does not allow this", ai.role)
	}
	return Errorf(http.StatusForbidden, CodeForbidden, "this token lacks the %q scope", scope)
}

// handle registers an API endpoint with authentication and scope checks.
// scope "" means public; "read" also accepts any authenticated caller.
func (s *Server) handle(pattern, scope string, f apiFunc) {
	s.routeList = append(s.routeList, routeInfo{pattern, scope})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scope != "" {
			ai, err := s.authenticate(r)
			if err != nil {
				writeErrorFor(w, r, err)
				return
			}
			if !ai.scopes[scope] && !ai.scopes["admin"] {
				writeErrorFor(w, r, scopeError(ai, scope))
				return
			}
			if ai.group != nil && !groupRoutes[pattern] {
				writeErrorFor(w, r, errGroupOnly)
				return
			}
			if ai.session && r.Method != http.MethodGet && r.Method != http.MethodHead {
				if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Unagikuma-CSRF")), []byte(ai.csrf)) != 1 {
					writeErrorFor(w, r, Errorf(http.StatusForbidden, CodeForbidden, "missing or wrong CSRF token"))
					return
				}
			}
			key := "u:" + ai.user
			if ai.token != "" {
				key = "t:" + ai.token
			}
			if ok, wait := s.api.Allow(key, s.now()); !ok {
				writeErrorFor(w, r, rateLimited(wait))
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ai))
		}
		if (scope == "write" || scope == "admin") && r.Method != "GET" && r.Method != "HEAD" && !s.app.ConfigAuthority() && sharedConfigRoute(r.URL.Path) {
			writeErrorFor(w, r, Errorf(http.StatusConflict, CodeConflict, "configuration is read-only on this site"))
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
				writeErrorFor(w, r, err)
				return
			}
			if audit {
				s.writeAudit(pattern, r, authOf(r), rec, v)
			}
			if r.Method == http.MethodDelete {
				deleted(w, v)
				return
			}
			if v == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, http.StatusOK, listEnvelope(v))
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

// handleRaw registers an API endpoint that does not go through handle:
// it authenticates itself (or is public, scope ""). The registration is
// recorded in rawRoutes so the route table stays enumerable for the
// OpenAPI drift test.
func (s *Server) handleRaw(pattern, scope string, h http.Handler) {
	s.rawRoutes = append(s.rawRoutes, routeInfo{pattern, scope})
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
	c, err := r.Cookie(s.sessionCookie())
	if err != nil {
		if n, _ := st.UserCount(); n == 0 {
			return nil, Errorf(http.StatusUnauthorized, CodeSetupRequired, "setup required")
		}
		return nil, errUnauthorized
	}
	se, err := st.SessionByID(c.Value)
	if err != nil {
		// A cookie left by an earlier installation on the same address
		// must not hide that this one still needs its first account.
		if n, _ := st.UserCount(); n == 0 {
			return nil, Errorf(http.StatusUnauthorized, CodeSetupRequired, "setup required")
		}
		return nil, errUnauthorized
	}
	ai := &authInfo{user: se.User.Username, userID: se.User.ID, csrf: se.CSRF, session: true, sessionTag: sessionTag(c.Value),
		scopes: roleScopes(se.User.Role), role: se.User.Role, areas: se.User.Areas, groups: se.User.Groups}
	if gs := groupScopeFor(s.app.EffectiveConfig(), se.User.Role, se.User.Groups); gs != nil {
		ai.group, ai.scopes = gs, gs.scopes()
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
	if strings.HasSuffix(path, "/pause") || strings.HasSuffix(path, "/bulk_pause") {
		return false
	}
	if strings.HasSuffix(path, "/test") || strings.HasSuffix(path, "/predict") || strings.HasSuffix(path, "/push-token") || strings.HasSuffix(path, "/push/rotate") {
		return false
	}
	for _, p := range []string{"/api/v1/config", "/api/v1/monitors", "/api/v1/channels", "/api/v1/policies", "/api/v1/teams", "/api/v1/people", "/api/v1/tunables", "/api/v1/maintenance", "/api/v1/nodes", "/api/v1/status_pages", "/api/v1/import"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
