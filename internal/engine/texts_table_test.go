package engine

import (
	"strings"
	"testing"
	"time"
)

// Every system message id renders a text, in English and in another
// language, and none falls back to the id itself.
func TestEveryTextRenders(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	cases := map[string][]any{
		"failed_and_recovered_while_down": {now, now},
		"recovered_after_flaps":           {2},
		"all_failed":                      {"p", "title"},
		"digest_title":                    {3}, "digest_service_title": {3}, "digest_network_title": {3},
		"digest_segment":     {3, "10.0.0.0/24"},
		"digest_dns":         {3},
		"backstop_title":     {3},
		"backstop_body":      {3, 2, "a, b"},
		"digest_body":        {[]string{"a", "b"}, "https://x/"},
		"node_offline_title": {"bj"}, "node_offline_detail": {now},
		"maint_start": {"work", now, 2}, "maint_end": {"work"},
		"node_witnesses": {1, 2}, "peer_unreachable_title": {"bj"}, "peer_unreachable": {"bj"},
		"minority_title": {}, "two_masters_title": {}, "two_masters": {"b", 2}, "minority": {1, 3, "a"},
		"node_back": {"bj"}, "master_unreachable": {"m", now}, "storage_failing": {now, "disk full", 3},
		"system_resolved": {"x"}, "system_resolved_abnormal_exit": {}, "system_title_abnormal_exit": {},
		"anomaly_" + AnomalyUplink: {"gw", true}, "anomaly_end_" + AnomalyDNS: {}, "anomaly_end_x": {},
		"outage_summary": {now, now.Add(time.Hour), 2, []string{"a", "b"}}, "outage_unsent": {3},
		"hold_released": {now, time.Minute, 2}, "hold_note": {now}, "system_notice_title": {}, "restarted": {},
		"abnormal_exit": {now, time.Minute}, "overload": {0.9, 3}, "reminder_title": {2}, "report_more": {3},
		"report_hosts_down": {2, "a, b"}, "report_services": {2},
		"report_service_line": {"web", "critical", now, time.Hour, "li", "HTTP 500"},
		"report_summary":      {2, 5, map[string]int{"ok": 4, "critical": 1}},
		"report_title":        {1, 2, 1, 5}, "report_title_ok": {2, 5},
		"reminder_line": {"web", time.Hour, "li", 2},
	}
	for _, lang := range []string{"en", "zh"} {
		e := &Engine{cfg: Config{Lang: lang, Location: time.UTC}}
		for id, args := range cases {
			got := e.text(id, args...)
			if got == id || got == "" || strings.Contains(got, "{") {
				t.Errorf("%s %s: %q", lang, id, got)
			}
		}
	}
}
