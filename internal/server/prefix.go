package server

import (
	"bytes"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// Path-prefix reverse proxies: the UI's pages, assets and API calls are
// all relative to a <base> element the server injects into every HTML
// shell. At the root the base is "/", so nothing changes; behind a
// proxy that strips a prefix (and says so in X-Forwarded-Prefix), the
// base carries the prefix and the whole console — assets, fetches, the
// event stream, the session cookie's path — stays inside it. Like
// X-Forwarded-For, the header is honoured only from a trusted proxy:
// a client that could set it directly could otherwise move its own
// session cookie's scope.

// urlPrefix returns the honoured path prefix ("/uk"), or "".
func (s *Server) urlPrefix(r *http.Request) string {
	if s.opts.TrustedProxy == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !s.opts.TrustedProxy(ip) {
		return ""
	}
	p := strings.TrimSpace(r.Header.Get("X-Forwarded-Prefix"))
	if len(p) < 2 || p[0] != '/' || strings.HasSuffix(p, "/") ||
		strings.Contains(p, "..") || strings.Contains(p, "//") {
		return ""
	}
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '/' || c == '-' || c == '_' || c == '.' || c == '~') {
			return ""
		}
	}
	return p
}

// injectBase inserts the <base> element a shell's relative references
// resolve against: the honoured prefix, or the root.
func injectBase(page []byte, prefix string) []byte {
	base := `<base href="` + prefix + `/">`
	if i := bytes.Index(page, []byte("<head>")); i >= 0 {
		i += len("<head>")
		out := make([]byte, 0, len(page)+len(base))
		out = append(out, page[:i]...)
		out = append(out, base...)
		out = append(out, page[i:]...)
		return out
	}
	return page
}

// serveShell serves one of the HTML shells with its <base> injected for
// the request's honoured prefix. False means the file is not there and
// the caller should fall back to plain file serving.
func (s *Server) serveShell(w http.ResponseWriter, r *http.Request, name string) bool {
	b, err := fs.ReadFile(s.opts.Web, name)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(injectBase(b, s.urlPrefix(r)))
	return true
}
