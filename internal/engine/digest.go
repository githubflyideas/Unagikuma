package engine

import (
	"sort"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// A digest says what kind of problem it is, because the reader acts on
// that: services failing one by one need their owners now; a whole
// network segment, or name resolution, failing at once is the network
// team's — the services behind it are likely fine. Network causes are
// told apart from the monitors' own results: several monitors of one IP
// segment failing at the network level together, or monitors failing to
// resolve their names together.

// digestPart is one cause in a digest.
type digestPart struct {
	cause string // service | segment | dns
	label string // the segment ("198.51.100.0/24")
	lines []string
	ids   []int64
}

// classify splits the notices of a digest into service problems and
// network causes. A cause needs notify.group-min monitors; smaller groups
// stay service problems.
func (e *Engine) classify(q []*Intent) (service digestPart, network []digestPart) {
	min := max(2, e.cfg.Tunables.Int("notify.group-min"))
	service.cause = "service"
	segments := map[string]*digestPart{}
	dns := &digestPart{cause: "dns"}
	var order []string
	for _, in := range q {
		line := digestLine(in)
		var id int64
		if in.IncidentID != 0 && NoticeKind(in.Kind) != NoticeRecovery {
			id = in.IncidentID
		}
		rt := e.states[in.Message.Monitor]
		switch {
		case rt == nil || NoticeKind(in.Kind) == NoticeRecovery:
		case rt.LastReason == probe.ReasonDNSResolve:
			dns.lines, dns.ids = append(dns.lines, line), appendID(dns.ids, id)
			continue
		case reasonClass(rt.LastReason) == "network" && (strings.HasPrefix(rt.Domain, "4:") || strings.HasPrefix(rt.Domain, "6:")):
			p := segments[rt.Domain]
			if p == nil {
				p = &digestPart{cause: "segment", label: rt.Domain[2:]}
				segments[rt.Domain] = p
				order = append(order, rt.Domain)
			}
			p.lines, p.ids = append(p.lines, line), appendID(p.ids, id)
			continue
		}
		service.lines, service.ids = append(service.lines, line), appendID(service.ids, id)
	}
	sort.Strings(order)
	for _, k := range order {
		p := segments[k]
		if len(p.lines) >= min {
			network = append(network, *p)
			continue
		}
		service.lines, service.ids = append(service.lines, p.lines...), append(service.ids, p.ids...)
	}
	if len(dns.lines) >= min {
		network = append(network, *dns)
	} else {
		service.lines, service.ids = append(service.lines, dns.lines...), append(service.ids, dns.ids...)
	}
	return service, network
}

func appendID(ids []int64, id int64) []int64 {
	if id == 0 {
		return ids
	}
	return append(ids, id)
}

// networkBody describes the network causes of a digest.
func (e *Engine) networkBody(parts []digestPart) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if p.cause == "dns" {
			b.WriteString(e.text("digest_dns", len(p.lines)))
		} else {
			b.WriteString(e.text("digest_segment", len(p.lines), p.label))
		}
		b.WriteString("\n" + strings.Join(p.lines, "\n"))
	}
	return b.String()
}

func partsLen(parts []digestPart) int {
	n := 0
	for _, p := range parts {
		n += len(p.lines)
	}
	return n
}

// digestLine is one merged notice as people read it: what happened to
// which monitor, why and for how long ("Problem: web — connection
// refused (5m)").
func digestLine(in *Intent) string {
	m := in.Message
	name := m.Title
	if name == "" {
		name = m.Monitor
	}
	line := i18n.T(m.Lang, "msg.kind."+in.Kind) + ": " + name
	if m.Reason != "" && m.Reason != "ok" && NoticeKind(in.Kind) != NoticeRecovery {
		line += " — " + i18n.T(m.Lang, "reason."+m.Reason)
	}
	if m.Duration >= time.Minute {
		line += " (" + i18n.Duration(m.Duration.Round(time.Minute), m.Lang) + ")"
	}
	if m.Observations != nil {
		line += " — " + i18n.T(m.Lang, "msg.observations", "table", m.Observations.Summary(), "gateway", m.Observations.Gateway, "uplink", m.Observations.Uplink)
	}
	return line
}
