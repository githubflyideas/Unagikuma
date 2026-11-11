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
	var conditions []string
	for i, k := range e.keys {
		if err := gatusField(&m, &conditions, k, e.vals[i]); err != nil {
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

const gatusUnsupported = "unsupported endpoint/client field (including alerts, DNS, TLS files, proxy or authentication configuration)"

func yStr(n *ynode) (string, error) {
	if n.kind != yScalar {
		return "", errGatusShape
	}
	return n.value, nil
}

func yBool(n *ynode) (bool, error) {
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

// gatusField reads one field of an endpoint into m.
func gatusField(m *adapterMonitor, conditions *[]string, k string, v *ynode) error {
	var err error
	if dst := map[string]*string{"name": &m.name, "group": &m.group, "url": &m.target, "interval": &m.interval,
		"method": &m.method, "body": &m.body}[k]; dst != nil {
		*dst, err = yStr(v)
		return err
	}
	switch k {
	case "enabled":
		var on bool
		on, err = yBool(v)
		m.disabled = !on
	case "headers":
		return gatusHeaders(m, v)
	case "conditions":
		if v.kind != ySeq {
			return errGatusShape
		}
		for _, c := range v.items {
			s, err := yStr(c)
			if err != nil {
				return err
			}
			*conditions = append(*conditions, s)
		}
	case "client":
		return gatusClient(m, v)
	default:
		m.why = gatusUnsupported
	}
	return err
}

func gatusHeaders(m *adapterMonitor, v *ynode) error {
	if v.kind != yMap {
		return errGatusShape
	}
	m.headers = map[string]string{}
	for j, hk := range v.keys {
		var err error
		if m.headers[hk], err = yStr(v.vals[j]); err != nil {
			return err
		}
	}
	return nil
}

func gatusClient(m *adapterMonitor, v *ynode) error {
	if v.kind != yMap {
		return errGatusShape
	}
	for j, ck := range v.keys {
		var err error
		switch ck {
		case "insecure":
			m.insecure, err = yBool(v.vals[j])
		case "ignore-redirect":
			m.noFollow, err = yBool(v.vals[j])
		case "timeout":
			m.timeout, err = yStr(v.vals[j])
		default:
			m.why = gatusUnsupported
		}
		if err != nil {
			return err
		}
	}
	return nil
}
