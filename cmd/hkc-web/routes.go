package main

import (
	"io/fs"
	"log"
	"net/http"
)

// Маршруты собраны в одном месте, включая адреса для совместимости.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, currentVersion()) })
	mux.HandleFunc("GET /api/admin/updates", s.updateStatus)
	mux.HandleFunc("GET /api/admin/updates/progress", s.updateProgress)
	mux.HandleFunc("POST /api/admin/updates/check", s.updateStatus)
	mux.HandleFunc("POST /api/admin/updates/install", s.installUpdate)
	mux.HandleFunc("GET /api/auth/me", s.authorize(s.me))
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/logout", s.authorize(s.logout))
	mux.HandleFunc("GET /api/admin/overview", s.adminOverview)
	mux.HandleFunc("GET /api/admin/audit", s.adminAudit)
	mux.HandleFunc("GET /api/admin/backups", s.listBackups)
	mux.HandleFunc("POST /api/admin/backups", s.createBackup)
	mux.HandleFunc("POST /api/admin/backups/{name}/restore", s.restoreBackup)
	mux.HandleFunc("GET /api/admin/config", s.adminConfig)
	mux.HandleFunc("POST /api/admin/config/validate", s.validateConfig)
	mux.HandleFunc("PATCH /api/admin/config", s.updateConfig)
	mux.HandleFunc("DELETE /api/admin/config/{key}", s.deleteConfigKey)
	mux.HandleFunc("GET /api/admin/config/history", s.listConfigHistory)
	mux.HandleFunc("GET /api/admin/config/history/{name}/diff", s.diffConfigHistory)
	mux.HandleFunc("POST /api/admin/config/history/{name}/restore", s.restoreConfigHistory)
	mux.HandleFunc("GET /api/admin/diagnostic-bundle", s.diagnosticBundle)
	mux.HandleFunc("GET /api/admin/security", s.securityOverview)
	mux.HandleFunc("GET /api/admin/maintenance", s.getMaintenance)
	mux.HandleFunc("PUT /api/admin/maintenance", s.setMaintenance)
	mux.HandleFunc("GET /api/admin/schedules", s.listSchedules)
	mux.HandleFunc("POST /api/admin/schedules", s.createSchedule)
	mux.HandleFunc("DELETE /api/admin/schedules/{id}", s.deleteSchedule)
	mux.HandleFunc("POST /api/admin/diagnostics/{command}", s.adminDiagnosticCommand)
	mux.HandleFunc("POST /api/admin/terminal", s.adminTerminal)
	mux.HandleFunc("GET /api/admin/tokens", s.listAPITokens)
	mux.HandleFunc("POST /api/admin/tokens", s.createAPIToken)
	mux.HandleFunc("DELETE /api/admin/tokens/{id}", s.revokeAPIToken)
	mux.HandleFunc("GET /api/v1/status", s.apiAuthorize("read", s.status))
	mux.HandleFunc("GET /api/v1/logs", s.apiAuthorize("read", s.logs))
	mux.HandleFunc("POST /api/v1/bot/{action}", s.apiAuthorize("control", s.action))
	mux.HandleFunc("POST /api/admin/invites", s.createInvite)
	mux.HandleFunc("POST /api/admin/bot/{action}", s.adminBotAction)
	mux.HandleFunc("DELETE /api/admin/invites/{token}", s.revokeInvite)
	mux.HandleFunc("DELETE /api/admin/users/{username}", s.deleteUser)
	mux.HandleFunc("PATCH /api/admin/users/{username}/role", s.changeRole)
	mux.HandleFunc("GET /api/status", s.authorize(s.status))
	mux.HandleFunc("GET /api/insights", s.authorize(s.insights))
	mux.HandleFunc("GET /api/metrics", s.authorize(s.liveMetrics))
	mux.HandleFunc("GET /api/system", s.authorize(s.systemHealth))
	mux.HandleFunc("GET /api/system/history", s.authorize(s.systemHistory))
	mux.HandleFunc("GET /api/incidents", s.authorize(s.incidents))
	mux.HandleFunc("GET /api/diagnostics", s.authorize(s.diagnostics))
	mux.HandleFunc("GET /api/public/status", s.publicStatus)
	mux.HandleFunc("GET /api/logs", s.authorize(s.logs))
	mux.HandleFunc("GET /api/events", s.authorize(s.events))
	mux.HandleFunc("POST /api/bot/{action}", s.authorizeControl(s.action))

	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(assets)))

	return mux
}
