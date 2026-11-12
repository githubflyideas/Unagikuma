package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/githubflyideas/unagikuma/internal/app"
	"github.com/githubflyideas/unagikuma/internal/cluster"
	"github.com/githubflyideas/unagikuma/internal/license"
	"github.com/githubflyideas/unagikuma/internal/store"
)

func (s *Server) routes() {
	// Setup and session.
	s.handle("GET /api/v1/setup/status", "", s.setupStatus)
	s.handle("POST /api/v1/setup", "", s.setup)
	s.handle("GET /api/v1/session", "read", s.sessionGet)
	s.handle("POST /api/v1/session", "", s.sessionLogin)
	s.handle("DELETE /api/v1/session", "read", s.sessionLogout)
	s.handle("PUT /api/v1/session/password", "read", s.changePassword)

	// Overview and monitors.
	s.handle("GET /api/v1/overview", "read", s.overview)
	s.handle("GET /api/v1/monitors", "read", s.monitors)
	s.handle("GET /api/v1/monitors/heartbeats", "read", s.heartbeats)
	s.handle("GET /api/v1/monitors/{name}", "read", s.monitorDetail)
	s.handle("GET /api/v1/monitors/{name}/latency", "read", s.monitorLatency)
	s.handle("GET /api/v1/monitors/{name}/timeline", "read", s.monitorTimeline)
	s.handle("GET /api/v1/monitors/{name}/push", "read", s.monitorPush)
	s.handle("POST /api/v1/monitors/{name}/push/rotate", "write", s.monitorPushRotate)
	s.handle("POST /api/v1/monitors", "write", s.monitorCreate)
	s.handle("POST /api/v1/monitors/bulk-delete", "write", s.monitorBulkDelete)
	s.handle("POST /api/v1/monitors/bulk-pause", "write", s.monitorBulkPause)
	s.handle("PUT /api/v1/monitors/{name}", "write", s.monitorUpdate)
	s.handle("DELETE /api/v1/monitors/{name}", "write", s.monitorDelete)
	s.handle("POST /api/v1/monitors/{name}/check", "ack", s.monitorCheck)
	s.handle("POST /api/v1/monitors/{name}/pause", "write", s.monitorPause)
	s.handle("POST /api/v1/probe", "write", s.probeOnce)
	s.handle("POST /api/v1/quickadd/parse", "read", s.quickadd)
	s.handle("POST /api/v1/monitors/same-check", "read", s.sameCheck)

	// Incidents, events, notifications.
	s.handle("GET /api/v1/history/sites", "read", s.historySites)
	s.handle("GET /api/v1/incidents", "read", s.incidents)
	s.handle("GET /api/v1/incidents/{id}/report", "read", s.incidentReport)
	s.handle("POST /api/v1/incidents/{id}/ack", "ack", s.incidentAck)
	s.handle("DELETE /api/v1/incidents/{id}/ack", "ack", s.incidentUnack)
	s.handle("POST /api/v1/incidents/{id}/silence", "ack", s.incidentSilence)
	s.handle("POST /api/v1/incidents/bulk", "ack", s.incidentBulk)
	s.handle("GET /api/v1/events", "read", s.events)
	s.handle("GET /api/v1/notifications", "read", s.notifications)

	// Configuration.
	s.handle("GET /api/v1/config", "read", s.configGet)
	s.handle("PUT /api/v1/config", "write", global(s.configPut))
	s.handle("POST /api/v1/config/validate", "read", s.configValidate)
	s.handle("GET /api/v1/config/history", "read", s.configHistory)
	s.handle("GET /api/v1/config/diff", "read", s.configDiff)
	s.handle("POST /api/v1/config/rollback", "write", global(s.configRollback))
	s.handle("POST /api/v1/config/reload", "write", global(s.configReload))

	// Channels and policies.
	s.handle("GET /api/v1/channel-kinds", "read", s.channelKinds)
	s.handle("GET /api/v1/channels", "read", s.channels)
	s.handle("POST /api/v1/channels", "admin", global(s.channelSave))
	s.handle("DELETE /api/v1/channels/{name}", "admin", global(s.channelDelete))
	s.handle("POST /api/v1/channels/test", "admin", global(s.channelTest))
	s.handle("GET /api/v1/changes", "read", s.changes)
	s.handle("POST /api/v1/changes", "write", s.changeCreate)
	s.handle("PUT /api/v1/changes/{id}", "write", s.changeUpdate)
	s.handle("POST /api/v1/changes/{id}/finish", "write", s.changeFinish)
	s.handle("GET /api/v1/changes/templates", "read", s.changeTemplates)
	s.handle("PUT /api/v1/changes/templates", "admin", s.changeTemplatesPut)
	s.handle("GET /api/v1/people", "read", s.people)
	s.handle("POST /api/v1/people", "admin", global(s.personSave))
	s.handle("PUT /api/v1/people/{id}", "admin", global(s.personSave))
	s.handle("DELETE /api/v1/people/{id}", "admin", global(s.personDelete))
	s.handle("POST /api/v1/people/{id}/left", "admin", global(s.personLeft))
	s.handle("POST /api/v1/people/{id}/status", "admin", global(s.personStatus))
	s.handle("POST /api/v1/people/{id}/report", "admin", global(s.personReport))
	s.handle("POST /api/v1/people/import", "admin", global(s.peopleImport))
	s.handle("POST /api/v1/people/lookup", "read", s.peopleLookup)
	s.handle("DELETE /api/v1/bounces", "admin", global(s.bounceClear))
	s.handle("GET /api/v1/teams", "read", s.teams)
	s.handle("POST /api/v1/teams", "admin", s.teamSave)
	s.handle("DELETE /api/v1/teams/{name}", "admin", s.teamDelete)
	s.handle("GET /api/v1/regions", "read", s.regions)
	s.handle("POST /api/v1/regions", "admin", s.regionSave)
	s.handle("DELETE /api/v1/regions/{name}", "admin", s.regionDelete)
	s.handle("POST /api/v1/monitors/move", "admin", s.monitorsMove)
	s.handle("GET /api/v1/policies", "read", s.policies)
	s.handle("POST /api/v1/policies", "admin", global(s.policySave))
	s.handle("POST /api/v1/policies/{name}/test", "admin", global(s.policyTest))
	s.handle("POST /api/v1/policies/{name}/predict", "read", s.policyPredict)

	// Settings.
	s.handle("GET /api/v1/tunables", "read", s.tunablesGet)
	s.handle("PUT /api/v1/tunables", "admin", s.tunablesPut)
	s.handle("GET /api/v1/keywords", "read", s.keywordsList)
	s.handle("GET /api/v1/system", "read", s.system)
	s.handle("GET /api/v1/settings", "read", s.settingsGet)
	s.handle("PUT /api/v1/settings", "admin", s.settingsPut)
	s.handle("POST /api/v1/settings/heartbeat/test", "admin", s.heartbeatTest)
	s.handle("GET /api/v1/preferences", "read", s.prefsGet)
	s.handle("PUT /api/v1/preferences", "read", s.prefsPut)
	s.handle("GET /api/v1/hosts", "read", s.hosts)
	s.handle("GET /api/v1/reports/availability", "read", s.availability)
	s.handle("GET /api/v1/reports/summary", "read", s.summary)
	s.handle("GET /api/v1/expiry", "read", s.expiry)
	s.handle("GET /api/v1/reports/daily", "read", s.dailyReport)
	s.handle("POST /api/v1/reports/daily/send", "write", global(s.sendDailyReport))
	s.handle("POST /api/v1/import/{source}", "admin", s.kumaImport)
	s.handle("GET /api/v1/status-pages", "read", s.statusPages)
	s.handle("GET /api/v1/status-support", "admin", s.supportGet)
	s.handle("PUT /api/v1/status-support", "admin", requireFeature(s, license.FeatBranding, s.supportPut))
	s.handle("POST /api/v1/status-pages", "write", global(s.statusPageCreate))
	s.handle("PUT /api/v1/status-pages/{name}", "write", global(s.statusPageUpdate))
	s.handle("DELETE /api/v1/status-pages/{name}", "write", global(s.statusPageDelete))
	s.handle("GET /api/v1/status-pages/{name}/announcements", "read", s.announcements)
	s.handle("POST /api/v1/status-pages/{name}/announcements", "write", global(s.announcementCreate))
	s.handle("PUT /api/v1/announcements/{id}", "write", global(s.announcementUpdate))
	s.handle("DELETE /api/v1/announcements/{id}", "write", global(s.announcementDelete))
	s.handle("GET /api/v1/maintenance", "read", s.maintList)
	s.handle("POST /api/v1/maintenance", "write", s.maintSave)
	s.handle("DELETE /api/v1/maintenance/{name}", "write", s.maintDelete)
	s.handle("POST /api/v1/cluster/join-codes", "admin", s.joinCode)
	s.handle("POST /api/v1/cluster/join", "admin", s.clusterJoin)
	s.handle("GET /api/v1/cluster/agent", "read", s.clusterAgent)
	s.handle("DELETE /api/v1/cluster/agent", "admin", s.clusterLeave)
	s.handle("POST /api/v1/cluster/promote", "admin", s.clusterPromote)
	s.handle("GET /api/v1/cluster/site-local", "admin", s.siteLocal)
	s.handle("GET /api/v1/cluster/diverged", "admin", s.diverged)
	s.handle("DELETE /api/v1/cluster/diverged/{id}", "admin", s.divergedDismiss)
	s.handle("PUT /api/v1/cluster/site-local", "admin", s.siteLocalSave)
	s.handle("POST /api/v1/nodes/{name}/handover", "admin", s.nodeHandover)
	s.handle("GET /api/v1/nodes", "read", s.nodesList)
	s.handle("POST /api/v1/nodes", "admin", s.nodeAdd)
	s.handle("POST /api/v1/nodes/{name}/token", "admin", s.nodeToken)
	s.handle("DELETE /api/v1/nodes/{name}/token", "admin", s.nodeRevoke)
	s.handle("DELETE /api/v1/nodes/{name}", "admin", s.nodeDelete)
	s.handle("PUT /api/v1/nodes/{name}", "admin", s.nodeUpdate)
	s.handle("GET /api/v1/users", "admin", s.usersList)
	s.handle("GET /api/v1/scripts", "read", s.scripts)
	s.handle("GET /api/v1/audit", "admin", requireFeature(s, license.FeatAudit, s.auditLog))
	s.handle("GET /api/v1/audit/export", "admin", requireFeature(s, license.FeatAudit, s.auditExport))
	s.handle("GET /api/v1/audit/verify", "admin", requireFeature(s, license.FeatAudit, s.auditVerify))
	s.handle("GET /api/v1/license", "read", s.licenseGet)
	s.handle("GET /api/v1/brand", "", s.brandGet)
	s.handle("GET /api/v1/themes", "admin", s.themesList)
	s.handle("POST /api/v1/themes", "admin", s.themesUpload)
	s.handle("PUT /api/v1/themes/active", "admin", s.themesActivate)
	s.handle("DELETE /api/v1/themes/{id}", "admin", s.themesDelete)
	s.handle("GET /api/v1/themes/{id}/export", "admin", s.themesExport)
	s.handle("GET /api/v1/themes/{id}/image/{which}", "admin", s.themesImage)
	s.handle("PUT /api/v1/license", "admin", s.licensePut)
	s.handle("DELETE /api/v1/license", "admin", s.licenseDelete)
	s.handle("POST /api/v1/users", "admin", s.userCreate)
	s.handle("PUT /api/v1/users/{id}", "admin", s.userUpdate)
	s.handle("DELETE /api/v1/users/{id}", "admin", s.userDelete)
	s.handle("GET /api/v1/tokens", "admin", s.tokensList)
	s.handle("POST /api/v1/tokens", "admin", s.tokenCreate)
	s.handle("DELETE /api/v1/tokens/{id}", "admin", s.tokenDelete)
	s.handle("GET /api/v1/secrets", "admin", s.secretsList)
	s.handle("PUT /api/v1/secrets/{name}", "admin", s.secretPut)
	s.handle("DELETE /api/v1/secrets/{name}", "admin", s.secretDelete)
	s.handle("GET /api/v1/backup", "admin", s.backup)
	s.handle("POST /api/v1/backup", "admin", s.backup)
	s.handle("GET /api/v1/languages", "", s.languages)
	s.mux.HandleFunc("GET /api/v1/i18n/{lang}", s.catalog)

	// Live stream.
	s.mux.Handle("GET /api/v1/stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ai, err := s.authenticate(r)
		if err != nil {
			writeError(w, err)
			return
		}
		// Every account but an administrator's goes through the team
		// filter, which follows configuration changes: one added to a
		// team while connected stops seeing other teams' events at once.
		if ai.session && ai.role != store.RoleAdmin {
			s.hub.ServeFiltered(w, r, s.teamStream(ai.user, ai.role))
			return
		}
		s.hub.ServeHTTP(w, r)
	}))

	// Public endpoints.
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /metrics", s.metrics)
	s.mux.HandleFunc("GET "+cluster.ConnectPath, s.agentConnect)
	s.mux.HandleFunc("GET "+app.SiteObservationPath, s.app.ServeSiteObservations)
	s.mux.HandleFunc("POST "+app.SiteIncidentActionPath, s.app.ServeSiteIncidentAction)
	s.mux.HandleFunc("GET "+cluster.StatePath, s.agentState)
	s.mux.HandleFunc("GET "+cluster.MergePath, s.agentMerge)
	s.mux.HandleFunc("POST "+cluster.MergePath, s.agentMerge)
	s.mux.HandleFunc("POST "+cluster.DivergedPath, s.agentDiverged)
	s.mux.HandleFunc("GET "+cluster.ReachPath, func(w http.ResponseWriter, r *http.Request) {
		cluster.ServeReach(w, s.app.FullReachView())
	})
	s.mux.HandleFunc("POST "+cluster.RetirePath, s.agentRetire)
	s.mux.HandleFunc("POST "+cluster.RejoinOfferPath, s.agentRejoinOffer)
	s.mux.HandleFunc("POST "+cluster.JoinPath, s.agentJoin)
	s.mux.HandleFunc("GET /api/v1/status/{name}", s.statusPageJSON)
	s.mux.HandleFunc("POST /api/v1/status/{name}/subscribe", s.subscribe)
	s.mux.HandleFunc("POST /api/v1/status/{name}/confirm", s.subscription)
	s.mux.HandleFunc("POST /api/v1/status/{name}/unsubscribe", s.subscription)
	s.mux.HandleFunc("GET /status/{name}/feed.atom", s.feed)
	s.mux.HandleFunc("GET /badge/{monitor}/{kind}", s.badge)
	for _, p := range []string{"/api/push/{token}", "/push/{token}"} {
		s.mux.HandleFunc("GET "+p, s.pushHandler)
		s.mux.HandleFunc("POST "+p, s.pushHandler)
	}
	s.mux.Handle("GET /api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, notFound("endpoint"))
	}))

	// UI.
	if s.opts.Web != nil {
		files := http.FileServerFS(s.opts.Web)
		s.mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := strings.TrimPrefix(r.URL.Path, "/")
			if strings.HasPrefix(p, "status/") && !strings.Contains(strings.TrimPrefix(p, "status/"), ".") {
				if s.serveStatusHTML(w, r, strings.Trim(strings.TrimPrefix(p, "status/"), "/")) {
					return
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/status.html"
				w.Header().Set("Cache-Control", "no-cache")
				files.ServeHTTP(w, r2)
				return
			}
			if (p == "logo.png" && s.brandImage(w, r, "logo")) || (p == "favicon.png" && s.brandImage(w, r, "favicon")) {
				return
			}
			if p == "wall" || p == "wall/" || p == "summary" || p == "summary/" {
				// The wall screen and the managers' summary; they read the
				// API with a token from the URL fragment, which never
				// reaches this server.
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + strings.Trim(p, "/") + ".html"
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Referrer-Policy", "no-referrer")
				files.ServeHTTP(w, r2)
				return
			}
			if p == "" || p == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			} else if _, err := fs.Stat(s.opts.Web, p); err != nil {
				writeError(w, notFound("page"))
				return
			} else if strings.HasPrefix(p, "vendor/") {
				w.Header().Set("Cache-Control", "public, max-age=86400")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
		}))
	}
}
