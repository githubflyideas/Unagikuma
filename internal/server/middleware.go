package server

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/ratelimit"
)

// securityHeaders applies the headers every response carries. The UI is
// served from the same origin with no inline scripts or styles, so the
// content security policy can be strict.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a handler panic into a 500 instead of a dropped
// connection, and logs the stack.
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("handler panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeError(w, nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// apiCatchAll is the pattern that answers unknown GET /api/ addresses.
const apiCatchAll = "GET /api/"

// methodGuard answers a method that an address does not accept with a
// JSON 405 and the Allow header, and an unknown address with a JSON 404.
// The mux's own answers are plain text, and its GET /api/ catch-all would
// otherwise call a write-only endpoint "not found".
func (s *Server) methodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pat := s.mux.Handler(r); pat != "" && pat != apiCatchAll {
			next.ServeHTTP(w, r)
			return
		}
		if allow := s.allowedMethods(r); len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			writeErrorFor(w, r, Errorf(http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method %s is not allowed here (allowed: %s)", r.Method, strings.Join(allow, ", ")))
			return
		}
		writeErrorFor(w, r, notFound("endpoint"))
	})
}

// allowedMethods lists the methods some endpoint accepts at r's address.
func (s *Server) allowedMethods(r *http.Request) []string {
	var allow []string
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r2 := r.Clone(r.Context())
		r2.Method = m
		if _, pat := s.mux.Handler(r2); pat != "" && pat != apiCatchAll {
			allow = append(allow, m)
		}
	}
	return allow
}

// clientIP returns the remote address without port. Proxy headers are
// honoured only when the direct peer is a trusted proxy.
func clientIP(r *http.Request, trusted func(net.IP) bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if trusted != nil {
		if ip := net.ParseIP(host); ip != nil && trusted(ip) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				// The right-most entry is the one our proxy appended.
				parts := strings.Split(xff, ",")
				if c := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(c) != nil {
					return c
				}
			}
		}
	}
	return host
}

// limiter rate-limits requests per key (token prefix or client IP).
type limiter struct {
	keyed *ratelimit.Keyed
	now   func() time.Time
	key   func(*http.Request) string
}

func (l *limiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := l.keyed.Allow(l.key(r), l.now()); !ok {
			writeError(w, rateLimited(wait))
			return
		}
		next.ServeHTTP(w, r)
	})
}
