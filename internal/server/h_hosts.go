package server

import (
	"net/http"

	"github.com/githubflyideas/unagikuma/internal/hostview"
)

// hostJSON is a host: the services (monitors) checking one device or
// address, with a host state in the Nagios sense.
type hostJSON = hostview.Host

func (s *Server) hostList(req *http.Request) []hostJSON {
	cfg := s.app.EffectiveConfig()
	status := map[string]string{}
	for _, r := range s.app.Engine().Rows() {
		status[r.Name] = r.Status
	}
	svcs := make([]hostview.Service, 0, len(cfg.Monitors))
	for _, m := range cfg.Monitors {
		if !seesMonitor(req, m.Name) {
			continue
		}
		svcs = append(svcs, hostview.Service{Name: m.Name, Host: m.HostKey(), Address: m.Host(), Area: m.Area, Group: m.Group,
			Ping: m.Type == "PING", Status: status[m.Name]})
	}
	return hostview.Build(svcs)
}

func (s *Server) hosts(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"items": s.hostList(r)}, nil
}
