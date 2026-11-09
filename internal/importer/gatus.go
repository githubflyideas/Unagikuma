package importer

import (
	"errors"
	"regexp"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/config"
)

var gatusStatus = regexp.MustCompile(`^\[STATUS\]\s*==\s*([1-5][0-9][0-9])$`)

var errGatusShape = errors.New("Gatus document requires an endpoints array with typed fields")

// Gatus imports a single local YAML document, never resolving includes or env.
func Gatus(data []byte, existing *config.Config, o Options) (*Result, error) {
	root, err := boundedYAML(data)
	if err != nil {
		return nil, err
	}
	if root.kind != yMap {
		return nil, errGatusShape
	}
	eps := root.get("endpoints")
	if eps == nil || eps.kind != ySeq {
		return nil, errGatusShape
	}
	if len(eps.items) > 1000 {
		return nil, errors.New("maximum 1000 endpoints")
	}
	var mons []adapterMonitor
	for _, e := range eps.items {
		m, err := gatusEndpoint(e)
		if err != nil {
			return nil, err
		}
		mons = append(mons, m)
	}
	r := adapterResult("Gatus", mons, existing, o)
	if len(root.keys) > 1 {
		r.add(Item{Kind: "configuration", Source: "top-level settings", Status: StatusLeftOut, Why: "Only endpoints are supported; global settings, external endpoints, suites, includes and alerting must be migrated separately."})
		r.finish()
	}
	return r, nil
}

// gatusEndpoint maps one endpoint. Wrongly typed known fields make the
// whole file unreadable; unknown fields block just this endpoint.
func gatusEndpoint(e *ynode) (adapterMonitor, error) {
	var m adapterMonitor
	if e.kind != yMap {
		return m, errGatusShape
	}
	str := func(n *ynode) (string, error) {
		if n.kind != yScalar {
			return "", errGatusShape
		}
		return n.value, nil
	}
	boolean := func(n *ynode) (bool, error) {
		if n.kind != yScalar || n.quoted {
			return false, errGatusShape
		}
		switch strings.ToLower(n.value) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return false, errGatusShape
	}
	var conditions []string
	var err error
	for i, k := range e.keys {
		v := e.vals[i]
		switch k {
		case "name":
			m.name, err = str(v)
		case "group":
			m.group, err = str(v)
		case "url":
			m.target, err = str(v)
		case "interval":
			m.interval, err = str(v)
		case "method":
			m.method, err = str(v)
		case "body":
			m.body, err = str(v)
		case "enabled":
			var on bool
			on, err = boolean(v)
			m.disabled = !on
		case "headers":
			if v.kind != yMap {
				return m, errGatusShape
			}
			m.headers = map[string]string{}
			for j, hk := range v.keys {
				if m.headers[hk], err = str(v.vals[j]); err != nil {
					return m, err
				}
			}
		case "conditions":
			if v.kind != ySeq {
				return m, errGatusShape
			}
			for _, c := range v.items {
				s, err := str(c)
				if err != nil {
					return m, err
				}
				conditions = append(conditions, s)
			}
		case "client":
			if v.kind != yMap {
				return m, errGatusShape
			}
			for j, ck := range v.keys {
				switch ck {
				case "insecure":
					m.insecure, err = boolean(v.vals[j])
				case "ignore-redirect":
					m.noFollow, err = boolean(v.vals[j])
				case "timeout":
					m.timeout, err = str(v.vals[j])
				default:
					m.why = "unsupported endpoint/client field (including alerts, DNS, TLS files, proxy or authentication configuration)"
				}
				if err != nil {
					return m, err
				}
			}
		default:
			m.why = "unsupported endpoint/client field (including alerts, DNS, TLS files, proxy or authentication configuration)"
		}
		if err != nil {
			return m, err
		}
	}
	if len(conditions) != 1 {
		m.why = "exactly one literal [STATUS] == N condition is supported"
	} else if match := gatusStatus.FindStringSubmatch(strings.TrimSpace(conditions[0])); match == nil {
		m.why = "unsupported condition: only literal [STATUS] == N is equivalent"
	} else {
		m.expect = match[1]
	}
	return m, nil
}
