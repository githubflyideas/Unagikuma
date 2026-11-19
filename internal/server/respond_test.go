package server

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A value JSON cannot carry turns into a 500 with a reason, never into an
// empty 200 that leaves the page blank.
func TestWriteJSONUnencodable(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]float64{"v": math.NaN()})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "could not be encoded") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]int{"v": 1})
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"v":1}` {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}
