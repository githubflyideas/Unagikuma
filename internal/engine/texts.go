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
	lang := e.cfg.Lang
	loc := e.cfg.Location
	if loc == nil {
		loc = time.Local
	}
	at := func(i int) string {
		t, _ := args[i].(time.Time)
		if t.IsZero() {
			return i18n.T(lang, "sys.unknown_time")
		}
		return t.In(loc).Format("2006-01-02 15:04")
	}
	hm := func(i int) string {
		t, _ := args[i].(time.Time)
		return t.In(loc).Format("15:04")
	}
	dur := func(d time.Duration) string { return i18n.Duration(d, lang) }
	switch {
	case id == "failed_and_recovered_while_down":
		return i18n.T(lang, "sys."+id, "t1", at(0), "t2", at(1))
	case id == "recovered_after_flaps":
		return i18n.N(lang, "msg.flaps", args[0].(int))
	case id == "all_failed":
		return i18n.T(lang, "sys.all_failed", "policy", args[0], "title", args[1])
	case id == "digest_title":
		return i18n.N(lang, "sys.digest_title", args[0].(int))
	case id == "digest_service_title":
		return i18n.N(lang, "sys.digest_service_title", args[0].(int))
	case id == "digest_network_title":
		return i18n.N(lang, "sys.digest_network_title", args[0].(int))
	case id == "digest_segment":
		return i18n.T(lang, "sys.digest_segment", "n", args[0], "segment", args[1])
	case id == "digest_dns":
		return i18n.T(lang, "sys.digest_dns", "n", args[0])
	case id == "backstop_title":
		return i18n.T(lang, "sys.backstop_title", "n", args[0])
	case id == "backstop_body":
		return i18n.T(lang, "sys.backstop_body", "n", args[0], "monitors", args[1], "names", args[2])
	case id == "digest_body":
		lines, _ := args[0].([]string)
		s := strings.Join(lines, "\n")
		if u, _ := args[1].(string); u != "" {
			s += "\n" + i18n.T(lang, "sys.digest_details", "url", strings.TrimRight(u, "/")+"/#/problems")
		}
		return s
	case id == "node_offline_title":
		return i18n.T(lang, "sys.node_offline", "node", args[0])
	case id == "node_offline_detail":
		return i18n.T(lang, "sys.node_offline_since", "time", at(0))
	case id == "maint_start":
		return i18n.T(lang, "sys.maint_start", "title", args[0], "end", at(1), "n", args[2])
	case id == "maint_end":
		return i18n.T(lang, "sys.maint_end", "title", args[0])
	case id == "node_witnesses":
		return i18n.T(lang, "sys.node_witnesses", "n", args[0], "total", args[1])
	case id == "peer_unreachable_title":
		return i18n.T(lang, "sys.peer_unreachable_title", "node", args[0])
	case id == "peer_unreachable":
		return i18n.T(lang, "sys.peer_unreachable", "node", args[0])
	case id == "minority_title":
		return i18n.T(lang, "sys.minority_title")
	case id == "two_masters_title":
		return i18n.T(lang, "sys.two_masters_title")
	case id == "two_masters":
		return i18n.T(lang, "sys.two_masters", "peer", args[0], "term", args[1])
	case id == "minority":
		return i18n.T(lang, "sys.minority", "reach", args[0], "total", args[1], "nodes", args[2])
	case id == "node_back":
		return i18n.T(lang, "sys.node_back", "node", args[0])
	case id == "master_unreachable":
		return i18n.T(lang, "sys.master_unreachable", "master", args[0], "time", at(1))
	case id == "storage_failing":
		return i18n.T(lang, "sys.storage_failing", "time", at(0), "error", args[1], "lost", args[2])
	case id == "system_resolved":
		return i18n.T(lang, "sys.resolved", "title", args[0])
	case strings.HasPrefix(id, "system_resolved_"):
		return i18n.T(lang, "sys.resolved."+strings.TrimPrefix(id, "system_resolved_"))
	case strings.HasPrefix(id, "system_title_"):
		return i18n.T(lang, "sys.title."+strings.TrimPrefix(id, "system_title_"))
	case id == "anomaly_"+AnomalyUplink:
		if args[1] == true {
			return i18n.T(lang, "sys.anomaly.uplink_down_suspected")
		}
		return i18n.T(lang, "sys.anomaly.uplink_down")
	case strings.HasPrefix(id, "anomaly_end_"):
		if id == "anomaly_end_"+AnomalyDNS {
			return i18n.T(lang, "sys.anomaly_end.dns")
		}
		return i18n.T(lang, "sys.anomaly_end.network")
	case strings.HasPrefix(id, "anomaly_"):
		return i18n.T(lang, "sys.anomaly."+strings.TrimPrefix(id, "anomaly_"), "gateway", args[0])
	case id == "outage_summary":
		from, to := args[0].(time.Time), args[1].(time.Time)
		failing := args[3].([]string)
		key := "sys.outage_summary_"
		if len(args) > 4 && args[4] == true {
			key = "sys.dns_summary_" // only the resolver failed
		}
		if len(failing) == 0 {
			return i18n.T(lang, key+"ok", "from", hm(0), "to", hm(1), "duration", dur(to.Sub(from)), "paused", args[2])
		}
		names := strings.Join(failing[:min(len(failing), 10)], ", ")
		if len(failing) > 10 {
			names += ", …"
		}
		return i18n.T(lang, key+"failing", "from", hm(0), "to", hm(1), "duration", dur(to.Sub(from)), "paused", args[2], "failing", len(failing), "names", names)
	case id == "outage_unsent":
		return i18n.T(lang, "sys.outage_unsent", "items", args[0])
	case id == "hold_released":
		return i18n.T(lang, "sys.hold_released", "time", hm(0), "duration", dur(args[1].(time.Duration)), "n", args[2])
	case id == "hold_note":
		return i18n.T(lang, "sys.hold_note", "time", hm(0))
	case strings.HasPrefix(id, "interrupted_"):
		return i18n.T(lang, "sys.interrupted."+strings.TrimPrefix(id, "interrupted_"), "duration", dur(args[0].(time.Duration)))
	case id == "system_notice_title":
		return i18n.T(lang, "sys.notice_title")
	case id == "restarted":
		return i18n.T(lang, "sys.restarted")
	case id == "abnormal_exit":
		return i18n.T(lang, "sys.abnormal_exit", "time", at(0), "duration", dur(args[1].(time.Duration)))
	case id == "overload":
		return i18n.T(lang, "sys.overload", "percent", fmt.Sprintf("%.0f", args[0].(float64)*100), "rounds", args[1])
	case id == "reminder_title":
		return i18n.N(lang, "sys.reminder_title", args[0].(int))
	case id == "report_more":
		return i18n.T(lang, "sys.report_more", "n", args[0])
	case strings.HasPrefix(id, "report_hosts_"):
		return i18n.T(lang, "sys."+id, "n", args[0], "list", args[1])
	case id == "report_services":
		return i18n.T(lang, "sys.report_services", "n", args[0])
	case id == "report_service_line":
		ack, detail := "", ""
		if who, _ := args[4].(string); who != "" {
			ack = i18n.T(lang, "sys.reminder_ack", "who", who)
		}
		if d, _ := args[5].(string); d != "" {
			detail = " — " + d
		}
		return i18n.T(lang, "sys.report_service_line", "title", args[0], "status", i18n.T(lang, "status."+args[1].(string)),
			"since", at(2), "duration", dur(args[3].(time.Duration)), "ack", ack) + detail
	case id == "report_summary":
		counts := args[2].(map[string]int)
		var parts []string
		for _, st := range []string{"ok", "critical", "degraded", "unknown", "unreachable", "maintenance", "pending", "paused"} {
			if counts[st] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", i18n.T(lang, "status."+st), counts[st]))
			}
		}
		return i18n.T(lang, "sys.report_summary", "hosts", args[0], "services", args[1], "states", strings.Join(parts, ", "))
	case id == "report_title":
		return i18n.T(lang, "sys.report_title", "down", args[0], "hosts", args[1], "bad", args[2], "services", args[3])
	case id == "report_title_ok":
		return i18n.T(lang, "sys.report_title_ok", "hosts", args[0], "services", args[1])
	case id == "reminder_line":
		ack := ""
		if who, _ := args[2].(string); who != "" {
			ack = i18n.T(lang, "sys.reminder_ack", "who", who)
		}
		return i18n.T(lang, "sys.reminder_line", "title", args[0], "duration", dur(args[1].(time.Duration)), "ack", ack, "count", args[3])
	}
	return id
}
