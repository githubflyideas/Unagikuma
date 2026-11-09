package server

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"
)

// healthz answers load balancers and container health checks without a
// login: 200 while the engine runs, 503 when it is stuck.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ok := make(chan struct{})
	go func() { s.app.Engine().Overview(); close(ok) }()
	code, state := http.StatusOK, "ok"
	select {
	case <-ok:
	case <-time.After(5 * time.Second):
		code, state = http.StatusServiceUnavailable, "engine not responding"
	}
	if s.app.SupersededBy() != nil {
		state = "replaced by a newer master"
	}
	out := map[string]any{"status": state, "version": s.app.Version(), "uptime_s": int(s.now().Sub(s.app.Started()).Seconds())}
	if wh := s.app.Store().WriteHealth(); wh.Failing {
		// Still serving (a restart would not free the disk), but not well:
		// results and events are not being kept.
		if code == http.StatusOK {
			out["status"] = "degraded"
		}
		out["storage"] = wh
	}
	writeJSON(w, code, out)
}

// metrics serves the Prometheus text format. Scrapers authenticate with
// an API token (read scope) unless api.metrics-public is on.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if !s.app.EffectiveConfig().Tunables.Bool("api.metrics-public") {
		ai, err := s.authenticate(r)
		if err != nil {
			writeError(w, err)
			return
		}
		if !ai.scopes["read"] && !ai.scopes["admin"] {
			writeError(w, Errorf(http.StatusForbidden, CodeForbidden, "the read scope is needed"))
			return
		}
	}
	var b strings.Builder
	help := func(name, typ, text string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, text, name, typ) }
	lbl := func(kv ...string) string {
		var parts []string
		for i := 0; i+1 < len(kv); i += 2 {
			v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
			parts = append(parts, fmt.Sprintf(`%s="%s"`, kv[i], v))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	cfg := s.app.EffectiveConfig()
	rows := s.app.Engine().Rows()

	help("unagikuma_build_info", "gauge", "Build version.")
	fmt.Fprintf(&b, "unagikuma_build_info%s 1\n", lbl("version", s.app.Version()))
	help("unagikuma_monitor_up", "gauge", "1 when the monitor is OK or degraded, 0 when critical or unreachable, -1 otherwise (pending, unknown, paused, maintenance).")
	help("unagikuma_monitor_latency_seconds", "gauge", "Latency of the last check.")
	help("unagikuma_monitor_status", "gauge", "1 for the monitor's current status.")
	statuses := []string{"ok", "degraded", "critical", "unreachable", "unknown", "pending", "paused", "maintenance"}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	for _, row := range rows {
		area, group := "", row.Group
		if m := cfg.FindMonitor(row.Name); m != nil {
			area = m.Area
		}
		l := lbl("monitor", row.Name, "type", row.Type, "group", group, "area", area)
		up := -1
		switch row.Status {
		case "ok", "degraded":
			up = 1
		case "critical", "unreachable":
			up = 0
		}
		fmt.Fprintf(&b, "unagikuma_monitor_up%s %d\n", l, up)
		fmt.Fprintf(&b, "unagikuma_monitor_latency_seconds%s %g\n", l, row.LatencyMS/1000)
		for _, st := range statuses {
			v := 0
			if row.Status == st {
				v = 1
			}
			fmt.Fprintf(&b, "unagikuma_monitor_status%s %d\n", lbl("monitor", row.Name, "status", st), v)
		}
	}
	ov := s.app.Engine().Overview()
	help("unagikuma_unhandled_problems", "gauge", "Problems not acknowledged or silenced.")
	fmt.Fprintf(&b, "unagikuma_unhandled_problems %d\n", ov.Unhandled)
	help("unagikuma_pending_notifications", "gauge", "Notifications waiting for delivery.")
	fmt.Fprintf(&b, "unagikuma_pending_notifications %d\n", ov.PendingIntents)
	help("unagikuma_node_online", "gauge", "1 when a probe node is connected.")
	for _, n := range s.app.Engine().Nodes() {
		v := 0
		if n.Online {
			v = 1
		}
		fmt.Fprintf(&b, "unagikuma_node_online%s %d\n", lbl("node", n.Node), v)
	}
	s.selfMetrics(&b, help, lbl)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// selfMetrics describes the monitoring host itself: whether checks start
// on time, whether history writes keep up, and what the process uses.
func (s *Server) selfMetrics(b *strings.Builder, help func(name, typ, text string), lbl func(kv ...string) string) {
	es := s.app.Engine().SelfStats()
	help("unagikuma_rounds_started_total", "counter", "Check rounds started.")
	fmt.Fprintf(b, "unagikuma_rounds_started_total %d\n", es.RoundsStarted)
	help("unagikuma_rounds_dropped_total", "counter", "Check rounds that could not start within their interval (all workers busy).")
	fmt.Fprintf(b, "unagikuma_rounds_dropped_total %d\n", es.RoundsDropped)
	help("unagikuma_round_start_delay_seconds_total", "counter", "Total delay of round starts after their due time; divide by rounds started for the average.")
	fmt.Fprintf(b, "unagikuma_round_start_delay_seconds_total %g\n", es.StartLagSeconds)

	st := s.app.Store()
	ws := st.WriteStats()
	help("unagikuma_store_flushes_total", "counter", "Batched writes of results and events.")
	fmt.Fprintf(b, "unagikuma_store_flushes_total %d\n", ws.Flushes)
	help("unagikuma_store_flush_seconds_total", "counter", "Time spent in batched writes.")
	fmt.Fprintf(b, "unagikuma_store_flush_seconds_total %g\n", ws.FlushSeconds)
	help("unagikuma_store_written_total", "counter", "Results, events and counters written.")
	fmt.Fprintf(b, "unagikuma_store_written_total %d\n", ws.Items)
	help("unagikuma_store_queued", "gauge", "Items waiting for the next batched write.")
	fmt.Fprintf(b, "unagikuma_store_queued %d\n", ws.Queued)
	core, hist := st.DBBytes()
	help("unagikuma_db_bytes", "gauge", "Size of the databases on disk, write-ahead log included.")
	fmt.Fprintf(b, "unagikuma_db_bytes%s %d\n", lbl("db", "core"), core)
	fmt.Fprintf(b, "unagikuma_db_bytes%s %d\n", lbl("db", "history"), hist)

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	help("unagikuma_process_memory_bytes", "gauge", "Memory obtained from the system by the Go runtime.")
	fmt.Fprintf(b, "unagikuma_process_memory_bytes %d\n", ms.Sys)
	help("unagikuma_process_heap_bytes", "gauge", "Bytes of allocated heap objects.")
	fmt.Fprintf(b, "unagikuma_process_heap_bytes %d\n", ms.HeapAlloc)
	help("unagikuma_goroutines", "gauge", "Goroutines running.")
	fmt.Fprintf(b, "unagikuma_goroutines %d\n", runtime.NumGoroutine())
	help("unagikuma_uptime_seconds", "gauge", "Seconds since the server started.")
	fmt.Fprintf(b, "unagikuma_uptime_seconds %d\n", int64(s.now().Sub(s.app.Started()).Seconds()))
	help("unagikuma_live_connections", "gauge", "Browsers connected to the live event stream.")
	fmt.Fprintf(b, "unagikuma_live_connections %d\n", s.hub.Clients())
}
