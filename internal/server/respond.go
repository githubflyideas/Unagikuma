// Package server is unagikuma's HTTP layer: REST /api/v1, the live event
// stream, authentication and setup, public status pages, badges, push
// endpoints and the embedded UI.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an APIError.
func Errorf(status int, code, format string, args ...any) *APIError {
	return &APIError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
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
	return &APIError{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: "too many requests",
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
