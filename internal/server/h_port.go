package server

import (
	"errors"
	"net/http"

	"github.com/githubflyideas/unagikuma/internal/app"
)

// The port, from the web UI: saved for the next start (see app.SetPort).

func (s *Server) portGet(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.PortInfo(), nil
}

func (s *Server) portPut(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Port int `json:"port"`
	}
	if err := decodeJSON(r, 1<<10, &req); err != nil {
		return nil, err
	}
	if err := s.app.SetPort(req.Port); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, app.ErrPortFixed) || errors.Is(err, app.ErrPortMaster) {
			code = http.StatusConflict
		}
		return nil, Errorf(code, CodeInvalid, "%v", err)
	}
	auditOf(r).note("port", req.Port).target = "port"
	return s.app.PortInfo(), nil
}

// restart restarts the program once the answer is sent.
func (s *Server) restart(w http.ResponseWriter, r *http.Request) (any, error) {
	if !s.app.PortInfo().CanRestart {
		return nil, Errorf(http.StatusConflict, CodeInvalid, "this program cannot restart itself here: restart it where it was started")
	}
	auditOf(r).target = "restart"
	go s.app.Restart()
	return map[string]any{"restarting": true}, nil
}
