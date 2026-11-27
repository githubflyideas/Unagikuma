package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// specRecorder wraps a test server's handler: every answer any test gets
// from it is checked against the OpenAPI document (status documented,
// JSON body matching the schema of that status), so the whole test suite
// keeps the document true. UK_SPEC_DUMP=file also writes each answer.
func specRecorder(t *testing.T, srv *Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		_, pattern := srv.mux.Handler(r)
		if pattern == "" || pattern == apiCatchAll {
			return
		}
		method, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, "/api/v1/") {
			return
		}
		if method == http.MethodGet && r.Method == http.MethodHead {
			return
		}
		body := rec.Body.Bytes()
		ctype := rec.Header().Get("Content-Type")
		if f := os.Getenv("UK_SPEC_DUMP"); f != "" {
			dumpAnswer(f, method, path, rec.Code, ctype, body)
		}
		if os.Getenv("UK_SPEC_CHECK") == "off" {
			return
		}
		if msg := checkAnswer(method, path, rec.Code, ctype, body); msg != "" {
			t.Errorf("%s %s (%s): %s", r.Method, r.URL.Path, pattern, msg)
		}
	})
}

var dumpMu sync.Mutex

func dumpAnswer(file, method, path string, code int, ctype string, body []byte) {
	dumpMu.Lock()
	defer dumpMu.Unlock()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	var js json.RawMessage
	if strings.Contains(ctype, "json") && json.Valid(body) && len(bytes.TrimSpace(body)) > 0 {
		js = body
	}
	b, _ := json.Marshal(map[string]any{"method": method, "path": path, "status": code, "ctype": ctype, "body": js})
	_, _ = f.Write(append(b, '\n'))
}
