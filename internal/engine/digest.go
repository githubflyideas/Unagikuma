package engine

import (
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
	"github.com/githubflyideas/unagikuma/internal/probe"
)

// A digest says what kind of problem it is, because the reader acts on
// that: services failing need their owners now; name resolution failing
// for several monitors at once is the network team's. Targets that do
// not answer are not grouped by address block: a block says nothing
// certain about which switch or link failed.

// digestPart is one cause in a digest.
type digestPart struct {
	cause string // service | dns
	lines []string
	ids   []int64
}

// classify splits the notices of a digest into service problems and the
// one network cause it can name with certainty: names failing to resolve
// together (notify.group-min or more). Targets that do not answer are not
// grouped by address: an address block says nothing certain about a
// switch or a link.
func (e *Engine) classify(q []*Intent) (service digestPart, network []digestPart) {
	min := max(2, e.cfg.Tunables.Int("notify.group-min"))
	service.cause = "service"
	dns := &digestPart{cause: "dns"}
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
		}
		service.lines, service.ids = append(service.lines, line), appendID(service.ids, id)
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
		b.WriteString(e.text("digest_dns", len(p.lines)))
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
		line += " — " + i18n.T(m.Lang, "msg.observations", "table", m.Observations.Summary())
	}
	return line
}
