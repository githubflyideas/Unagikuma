package server

import (
	"net/http"

	"github.com/githubflyideas/unagikuma/internal/probe"
)

var optionTypes = []string{"HTTP", "TCP", "PING", "UDP", "DNS", "NTP", "SNMP", "SYSLOG", "PUSH", "EXEC", "REDIS", "POSTGRES", "MYSQL", "GRPC", "DOCKER", "DOMAIN"}

// monitorOptions lists, per monitor type, the options the monitor form
// offers: the check's own ones, then those of the monitor that apply to
// that type. A push monitor waits instead of checking, so it has no
// timeout, retries or latency; a domain registration has no latency
// either.
func (s *Server) monitorOptions(w http.ResponseWriter, r *http.Request) (any, error) {
	out := map[string][]string{}
	for _, typ := range optionTypes {
		opts := probe.OptionsFor(typ)
		switch typ {
		case "PUSH":
			opts = append(opts, "period", "grace")
		case "DOMAIN":
			opts = append(opts, "timeout", "retry", "attempts", "recover-attempts")
		default:
			opts = append(opts, "timeout", "retry", "attempts", "recover-attempts", "degraded")
		}
		if typ != "PUSH" && typ != "EXEC" && typ != "DOMAIN" {
			opts = append(opts, "scope")
		}
		out[typ] = append(opts, "parent", "handler")
	}
	return out, nil
}
