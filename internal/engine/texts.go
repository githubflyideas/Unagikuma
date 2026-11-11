package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/githubflyideas/unagikuma/internal/i18n"
)

// text renders an engine-generated message in the configured language.
// Monitor notifications are rendered by the notify package from structured
// fields; these are the bodies of system messages, summaries and digests.
func (e *Engine) text(id string, args ...any) string {
	loc := e.cfg.Location
	if loc == nil {
		loc = time.Local
	}
	c := &textArgs{lang: e.cfg.Lang, loc: loc, args: args}
	if t, ok := plainTexts[id]; ok {
		kv := make([]any, 0, 2*len(t.names))
		for i, n := range t.names {
			kv = append(kv, n, args[i])
		}
		return i18n.T(c.lang, t.key, kv...)
	}
	if key, ok := countedTexts[id]; ok {
		return i18n.N(c.lang, key, args[0].(int))
	}
	if f, ok := customTexts[id]; ok {
		return f(c)
	}
	for _, p := range prefixTexts {
		if rest, ok := strings.CutPrefix(id, p.prefix); ok {
			return p.text(c, id, rest)
		}
	}
	return id
}

// textArgs are the language, time zone and arguments of one message.
type textArgs struct {
	lang string
	loc  *time.Location
	args []any
}

func (c *textArgs) at(i int) string {
	t, _ := c.args[i].(time.Time)
	if t.IsZero() {
		return i18n.T(c.lang, "sys.unknown_time")
	}
	return t.In(c.loc).Format("2006-01-02 15:04")
}

func (c *textArgs) hm(i int) string {
	t, _ := c.args[i].(time.Time)
	return t.In(c.loc).Format("15:04")
}

func (c *textArgs) dur(d time.Duration) string { return i18n.Duration(d, c.lang) }

func (c *textArgs) t(key string, kv ...any) string { return i18n.T(c.lang, key, kv...) }

// plainTexts fill a message's placeholders with its arguments in order.
var plainTexts = map[string]struct {
	key   string
	names []string
}{
	"all_failed":             {"sys.all_failed", []string{"policy", "title"}},
	"digest_segment":         {"sys.digest_segment", []string{"n", "segment"}},
	"digest_dns":             {"sys.digest_dns", []string{"n"}},
	"backstop_title":         {"sys.backstop_title", []string{"n"}},
	"backstop_body":          {"sys.backstop_body", []string{"n", "monitors", "names"}},
	"node_offline_title":     {"sys.node_offline", []string{"node"}},
	"maint_end":              {"sys.maint_end", []string{"title"}},
	"node_witnesses":         {"sys.node_witnesses", []string{"n", "total"}},
	"peer_unreachable_title": {"sys.peer_unreachable_title", []string{"node"}},
	"peer_unreachable":       {"sys.peer_unreachable", []string{"node"}},
	"minority_title":         {"sys.minority_title", nil},
	"two_masters_title":      {"sys.two_masters_title", nil},
	"two_masters":            {"sys.two_masters", []string{"peer", "term"}},
	"minority":               {"sys.minority", []string{"reach", "total", "nodes"}},
	"node_back":              {"sys.node_back", []string{"node"}},
	"system_resolved":        {"sys.resolved", []string{"title"}},
	"outage_unsent":          {"sys.outage_unsent", []string{"items"}},
	"system_notice_title":    {"sys.notice_title", nil},
	"restarted":              {"sys.restarted", nil},
	"report_more":            {"sys.report_more", []string{"n"}},
	"report_services":        {"sys.report_services", []string{"n"}},
	"report_title":           {"sys.report_title", []string{"down", "hosts", "bad", "services"}},
	"report_title_ok":        {"sys.report_title_ok", []string{"hosts", "services"}},
}

// countedTexts take their count as the first argument.
var countedTexts = map[string]string{
	"recovered_after_flaps": "msg.flaps", "digest_title": "sys.digest_title", "digest_service_title": "sys.digest_service_title",
	"digest_network_title": "sys.digest_network_title", "reminder_title": "sys.reminder_title",
}

var customTexts = map[string]func(c *textArgs) string{
	"failed_and_recovered_while_down": func(c *textArgs) string {
		return c.t("sys.failed_and_recovered_while_down", "t1", c.at(0), "t2", c.at(1))
	},
	"digest_body":         digestBody,
	"node_offline_detail": func(c *textArgs) string { return c.t("sys.node_offline_since", "time", c.at(0)) },
	"maint_start": func(c *textArgs) string {
		return c.t("sys.maint_start", "title", c.args[0], "end", c.at(1), "n", c.args[2])
	},
	"master_unreachable": func(c *textArgs) string {
		return c.t("sys.master_unreachable", "master", c.args[0], "time", c.at(1))
	},
	"storage_failing": func(c *textArgs) string {
		return c.t("sys.storage_failing", "time", c.at(0), "error", c.args[1], "lost", c.args[2])
	},
	"anomaly_" + AnomalyUplink: func(c *textArgs) string {
		if c.args[1] == true {
			return c.t("sys.anomaly.uplink_down_suspected")
		}
		return c.t("sys.anomaly.uplink_down")
	},
	"outage_summary": outageSummary,
	"hold_released": func(c *textArgs) string {
		return c.t("sys.hold_released", "time", c.hm(0), "duration", c.dur(c.args[1].(time.Duration)), "n", c.args[2])
	},
	"hold_note": func(c *textArgs) string { return c.t("sys.hold_note", "time", c.hm(0)) },
	"abnormal_exit": func(c *textArgs) string {
		return c.t("sys.abnormal_exit", "time", c.at(0), "duration", c.dur(c.args[1].(time.Duration)))
	},
	"overload": func(c *textArgs) string {
		return c.t("sys.overload", "percent", fmt.Sprintf("%.0f", c.args[0].(float64)*100), "rounds", c.args[1])
	},
	"report_service_line": reportServiceLine,
	"report_summary":      reportSummary,
	"reminder_line": func(c *textArgs) string {
		return c.t("sys.reminder_line", "title", c.args[0], "duration", c.dur(c.args[1].(time.Duration)), "ack", c.ack(2), "count", c.args[3])
	},
}

// prefixTexts are families of messages named by a prefix, in order.
var prefixTexts = []struct {
	prefix string
	text   func(c *textArgs, id, rest string) string
}{
	{"system_resolved_", func(c *textArgs, _, rest string) string { return c.t("sys.resolved." + rest) }},
	{"system_title_", func(c *textArgs, _, rest string) string { return c.t("sys.title." + rest) }},
	{"anomaly_end_", func(c *textArgs, id, _ string) string {
		if id == "anomaly_end_"+AnomalyDNS {
			return c.t("sys.anomaly_end.dns")
		}
		return c.t("sys.anomaly_end.network")
	}},
	{"anomaly_", func(c *textArgs, _, rest string) string { return c.t("sys.anomaly."+rest, "gateway", c.args[0]) }},
	{"interrupted_", func(c *textArgs, _, rest string) string {
		return c.t("sys.interrupted."+rest, "duration", c.dur(c.args[0].(time.Duration)))
	}},
	{"report_hosts_", func(c *textArgs, id, _ string) string { return c.t("sys."+id, "n", c.args[0], "list", c.args[1]) }},
}

// ack is "acknowledged by who" for the name in argument i, if any.
func (c *textArgs) ack(i int) string {
	if who, _ := c.args[i].(string); who != "" {
		return c.t("sys.reminder_ack", "who", who)
	}
	return ""
}

func digestBody(c *textArgs) string {
	lines, _ := c.args[0].([]string)
	s := strings.Join(lines, "\n")
	if u, _ := c.args[1].(string); u != "" {
		s += "\n" + c.t("sys.digest_details", "url", strings.TrimRight(u, "/")+"/#/problems")
	}
	return s
}

func outageSummary(c *textArgs) string {
	from, to := c.args[0].(time.Time), c.args[1].(time.Time)
	failing := c.args[3].([]string)
	key := "sys.outage_summary_"
	if len(c.args) > 4 && c.args[4] == true {
		key = "sys.dns_summary_" // only the resolver failed
	}
	if len(failing) == 0 {
		return c.t(key+"ok", "from", c.hm(0), "to", c.hm(1), "duration", c.dur(to.Sub(from)), "paused", c.args[2])
	}
	names := strings.Join(failing[:min(len(failing), 10)], ", ")
	if len(failing) > 10 {
		names += ", …"
	}
	return c.t(key+"failing", "from", c.hm(0), "to", c.hm(1), "duration", c.dur(to.Sub(from)), "paused", c.args[2], "failing", len(failing), "names", names)
}

func reportServiceLine(c *textArgs) string {
	detail := ""
	if d, _ := c.args[5].(string); d != "" {
		detail = " — " + d
	}
	return c.t("sys.report_service_line", "title", c.args[0], "status", c.t("status."+c.args[1].(string)),
		"since", c.at(2), "duration", c.dur(c.args[3].(time.Duration)), "ack", c.ack(4)) + detail
}

func reportSummary(c *textArgs) string {
	counts := c.args[2].(map[string]int)
	var parts []string
	for _, st := range []string{"ok", "critical", "degraded", "unknown", "unreachable", "maintenance", "pending", "paused"} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", c.t("status."+st), counts[st]))
		}
	}
	return c.t("sys.report_summary", "hosts", c.args[0], "services", c.args[1], "states", strings.Join(parts, ", "))
}
