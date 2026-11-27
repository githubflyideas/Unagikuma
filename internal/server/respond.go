// Package server is unagikuma's HTTP layer: REST /api/v1, the live event
// stream, authentication and setup, public status pages, badges, push
// endpoints and the embedded UI.
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/githubflyideas/unagikuma/internal/i18n"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"time"
)

// Error codes the UI understands.
const (
	CodeUnauthorized   = "unauthorized"
	CodeForbidden      = "forbidden"
	CodeNotFound       = "not_found"
	CodeInvalid        = "invalid"
	CodeConflict       = "conflict"
	CodeChanged        = "record_changed" // If-Match failed: someone else changed the record
	CodeConfigReadonly = "config_readonly"
	CodeRateLimited    = "rate_limited"
	CodeSetupRequired  = "setup_required"
	CodeInternal       = "internal"
	CodeTooLarge       = "too_large"
	// CodeMethodNotAllowed: the address exists, but not with this method
	// (the Allow header lists the ones it takes).
	CodeMethodNotAllowed = "method_not_allowed"
)

// Location points into monitors.conf for configuration errors.
type Location struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
}

// APIError is the error body of every failed request.
type APIError struct {
	Status   int       `json:"-"`
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	Detail   string    `json:"detail,omitempty"`
	Location *Location `json:"location,omitempty"`
	// Extra fields for specific codes (etag, patch, retry_after_s).
	Extra map[string]any `json:"-"`
	// format and args rebuild Message in the caller's language.
	format string
	args   []any
}

// localized returns e with its message in lang when the API catalog has
// the message; otherwise e itself.
func (e *APIError) localized(lang string) *APIError {
	if e == nil || e.format == "" || lang == "en" {
		return e
	}
	msg, ok := i18n.APIMessage(lang, e.format, e.args...)
	if !ok {
		return e
	}
	c := *e
	c.Message = msg
	return &c
}

// writeErrorFor writes err in the language the request asks for.
func writeErrorFor(w http.ResponseWriter, r *http.Request, err error) {
	var ae *APIError
	if errors.As(err, &ae) {
		w.Header().Set("Content-Language", requestLang(r))
		err = ae.localized(requestLang(r))
	}
	writeError(w, err)
}

// requestLang is the supported language the request prefers ("en" if none).
func requestLang(r *http.Request) string {
	return i18n.Normalize(firstLang(r.Header.Get("Accept-Language")))
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an APIError.
func Errorf(status int, code, format string, args ...any) *APIError {
	return &APIError{Status: status, Code: code, Message: fmt.Sprintf(format, args...), format: format, args: args}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	// Encoded before the status is sent: a value JSON cannot hold (NaN,
	// an infinity) must not turn into an empty "200 OK".
	b, err := encodeAnswer(v)
	if err != nil {
		status = http.StatusInternalServerError
		b, _ = json.Marshal(map[string]any{"code": CodeInternal, "message": "the answer could not be encoded: " + err.Error()})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, err error) {
	var ae *APIError
	if !errors.As(err, &ae) {
		ae = &APIError{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "internal error"}
	}
	body := map[string]any{"code": ae.Code, "message": ae.Message}
	if ae.Detail != "" {
		body["detail"] = ae.Detail
	}
	if ae.Location != nil {
		body["location"] = ae.Location
	}
	for k, v := range ae.Extra {
		body[k] = v
	}
	if ae.Code == CodeRateLimited {
		if s, ok := ae.Extra["retry_after_s"].(int); ok {
			w.Header().Set("Retry-After", strconv.Itoa(s))
		}
	}
	writeJSON(w, ae.Status, body)
}

// rateLimited builds the 429 error with a whole-second retry hint.
func rateLimited(wait time.Duration) *APIError {
	s := int((wait + time.Second - 1) / time.Second)
	return &APIError{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: "too many requests", format: "too many requests",
		Extra: map[string]any{"retry_after_s": max(1, s)}}
}

// decodeJSON reads a JSON body of at most maxBytes into v, rejecting
// unknown fields and trailing data so typos do not silently do nothing.
func decodeJSON(r *http.Request, maxBytes int64, v any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return Errorf(http.StatusRequestEntityTooLarge, CodeTooLarge, "request body larger than %d bytes", maxBytes)
		}
		return Errorf(http.StatusBadRequest, CodeInvalid, "invalid JSON body: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Errorf(http.StatusBadRequest, CodeInvalid, "unexpected data after the JSON body")
	}
	return nil
}

// listEnvelope puts a list answer into the one shape every list has:
// {"items": [...]} (never null), so clients can add fields later
// without a breaking change.
func listEnvelope(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return v
	}
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		v = reflect.MakeSlice(rv.Type(), 0, 0).Interface()
	}
	return map[string]any{"items": v}
}

// deleted answers a successful DELETE: 204 No Content, with the new
// configuration version in the ETag header when the deletion changed it.
func deleted(w http.ResponseWriter, v any) {
	if m, ok := v.(map[string]any); ok {
		if tag, ok := m["etag"].(string); ok && tag != "" {
			w.Header().Set("ETag", tag)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// encodeAnswer is the JSON of an answer, with every time in one form.
func encodeAnswer(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return normTimes(b), nil
}

// jsonTime matches a JSON string that is wholly an RFC 3339 time as
// encoding/json writes it (fraction of 0 to 9 digits, trailing zeros cut).
var jsonTime = regexp.MustCompile(`"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})"`)

// normTimes writes every time with exactly three fraction digits
// (milliseconds, the precision times are stored with), so that clients
// can parse and compare them as fixed-width strings.
func normTimes(b []byte) []byte {
	if !bytes.Contains(b, []byte(`T`)) {
		return b
	}
	return jsonTime.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := jsonTime.FindSubmatch(m)
		frac := string(sub[2])
		if frac == "" {
			frac = "."
		}
		frac = (frac + "000")[:4]
		out := make([]byte, 0, len(m)+4)
		out = append(out, '"')
		out = append(out, sub[1]...)
		out = append(out, frac...)
		out = append(out, sub[3]...)
		return append(out, '"')
	})
}
