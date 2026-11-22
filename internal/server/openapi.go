package server

import (
	_ "embed"
	"net/http"
)

// openapi.json is the machine-readable description of the /api/v1 API
// (API clause: external interfaces carry a published spec). It is a
// checked-in artifact; TestOpenAPIMatchesRoutes fails when it drifts
// from the routes the server actually registers.
//
//go:embed openapi.json
var openAPISpecJSON []byte

// openAPISpec serves the API description. It is public, like the
// sibling products' spec endpoints: the document names endpoints and
// the scope each one needs, but grants nothing by itself.
func (s *Server) openAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(openAPISpecJSON)
}
