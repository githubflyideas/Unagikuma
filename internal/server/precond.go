package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/config"
)

// Single records are written with optimistic concurrency: every record
// read from the API carries an etag, and a write that sends it back in
// If-Match fails with 412 when someone else changed that record in the
// meantime. Changes to other records do not conflict, so two operators
// editing different monitors never get in each other's way.

// errPrecondition is a failed If-Match on one record.
type errPrecondition struct{ current string }

func (e errPrecondition) Error() string { return "the record was changed by someone else; reload it" }

// ifMatch wraps a structured edit of record name with the request's
// If-Match check. It runs inside the edit, on the text being changed.
func ifMatch(r *http.Request, name string, change func(f *config.File) error) func(f *config.File) error {
	want := strings.TrimSpace(r.Header.Get("If-Match"))
	if want == "" {
		return change
	}
	return func(f *config.File) error {
		cur := f.RecordETag(name)
		if cur == "" || (want != "*" && !etagListHas(want, cur)) {
			return errPrecondition{cur}
		}
		return change(f)
	}
}

// etagListHas reports whether an If-Match list ("a", "b") holds tag.
func etagListHas(list, tag string) bool {
	for _, t := range strings.Split(list, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == tag {
			return true
		}
	}
	return false
}

// recordETags returns the etags of the records of the running file,
// computed once per version.
func (s *Server) recordETags() map[string]string {
	cur := s.app.Config()
	s.etags.mu.Lock()
	defer s.etags.mu.Unlock()
	if s.etags.sha != cur.SHA || s.etags.m == nil {
		f := cur.File
		if f == nil {
			f, _ = config.Parse("", []byte(cur.Text))
		}
		s.etags.sha, s.etags.m = cur.SHA, f.RecordETags()
	}
	return s.etags.m
}

type etagCache struct {
	mu  sync.Mutex
	sha string
	m   map[string]string
}

// savedRecord is the answer to a write of one record: the new file
// version and the record's new etag (absent once deleted).
func (s *Server) savedRecord(st app.ConfigState, name string) map[string]any {
	out := s.saved(st)
	if tag := s.recordETags()[name]; tag != "" {
		out["record_etag"] = tag
	}
	return out
}

// monitorParam reads the {name} of a monitor path: a name, or "id:N" for
// the stable id that survives renames (':' never appears in a name).
func (s *Server) monitorParam(r *http.Request) string {
	v := r.PathValue("name")
	if rest, ok := strings.CutPrefix(v, "id:"); ok {
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil {
			if name := s.app.Store().MonitorName(id); name != "" {
				return name
			}
		}
	}
	return v
}

// Creates sent with an Idempotency-Key header are remembered for
// api.idempotency-ttl: retrying the same request (a timeout, a lost
// answer) returns the first answer instead of applying it again. Only
// successful answers are kept, so a failed request can be retried as is.

type idemEntry struct {
	bodySHA [32]byte
	done    bool
	status  int
	body    []byte
	at      time.Time
}

type idemCache struct {
	mu sync.Mutex
	m  map[string]*idemEntry
}

const idemMaxEntries = 10000

var errIdemBusy = Errorf(http.StatusConflict, CodeConflict, "a request with this Idempotency-Key is still being processed")
var errIdemReused = Errorf(http.StatusUnprocessableEntity, CodeInvalid, "this Idempotency-Key was used for a different request")

// idempotent runs f once per (caller, path, key) and replays its answer.
// It returns false when the request carries no key and must run as usual.
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, caller string, run func(r *http.Request) (any, error), finish func(v any, err error)) bool {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || r.Method != http.MethodPost {
		return false
	}
	if len(key) > 255 {
		writeError(w, Errorf(http.StatusBadRequest, CodeInvalid, "Idempotency-Key is longer than 255 characters"))
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody()+1))
	if err != nil {
		writeError(w, Errorf(http.StatusBadRequest, CodeInvalid, "cannot read the request body"))
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256(body)
	id := caller + "\x00" + r.URL.Path + "\x00" + key
	now := s.now()
	ttl := s.app.EffectiveConfig().Tunables.Duration("api.idempotency-ttl")

	c := &s.idem
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*idemEntry{}
	}
	if e := c.m[id]; e != nil && now.Sub(e.at) < ttl {
		c.mu.Unlock()
		switch {
		case e.bodySHA != sum:
			writeError(w, errIdemReused)
		case !e.done:
			writeError(w, errIdemBusy)
		default:
			w.Header().Set("Idempotent-Replayed", "true")
			if e.status == http.StatusNoContent {
				w.WriteHeader(e.status)
			} else {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(e.status)
				_, _ = w.Write(e.body)
			}
		}
		return true
	}
	if len(c.m) >= idemMaxEntries {
		for k, e := range c.m {
			if now.Sub(e.at) >= ttl || e.done {
				delete(c.m, k)
			}
			if len(c.m) < idemMaxEntries/2 {
				break
			}
		}
	}
	e := &idemEntry{bodySHA: sum, at: now}
	c.m[id] = e
	c.mu.Unlock()

	v, err := run(r)
	finish(v, err)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		delete(c.m, id)
		return true
	}
	e.done, e.status = true, http.StatusNoContent
	if v != nil {
		e.status = http.StatusOK
		e.body, _ = json.Marshal(v)
		e.body = append(e.body, '\n')
	}
	return true
}
