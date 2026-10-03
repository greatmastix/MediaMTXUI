package app

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/auth"
)

// Access says who may call a route. Every route declares one; a test fails on any route that does not.
type Access string

// Access levels.
const (
	Public       Access = "public"        // anyone, signed in or not
	Streamer     Access = "streamer"      // any signed-in user, streamers included: the handler checks what they own
	Viewer       Access = "viewer"        // viewers, operators and admins (not streamers)
	Operator     Access = "operator"      // operators and admins
	Admin        Access = "admin"         // admins only
	PerOperation Access = "per-operation" // the MediaMTX API proxy: signed in, then the operations table decides
)

// Route is one registered endpoint.
type Route struct {
	Method  string // "*" for any method
	Pattern string
	Access  Access
	StepUp  bool // admin-level: the session proves who it is again after an hour
}

// Routes lists the public listener's registered routes (after Public has been built).
func (s *Server) Routes() []Route { return s.routes }

// Public is the handler for the public listener.
func (s *Server) Public() http.Handler {
	s.routes = nil
	r := chi.NewRouter()
	r.Use(s.recoverer, bodyDeadline, s.securityHeaders, s.resolver.Middleware, s.requestLog, s.unsafeGate, s.loadSession, s.watchProxy,
		s.d.Audit.Middleware(actor), s.csrf)

	r.Route("/api", func(api chi.Router) {
		api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "not found")
		})
		api.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		})
		s.handle(api, http.MethodGet, "/v1/health", Public, s.health)
		s.handle(api, http.MethodGet, "/v1/setup", Public, s.setupStatus)
		s.handle(api, http.MethodPost, "/v1/setup", Public, s.setupComplete)
		s.handle(api, http.MethodPost, "/v1/auth/login", Public, s.login)
		s.handle(api, http.MethodPost, "/v1/auth/login/code", Public, s.loginCode)
		s.handle(api, http.MethodPost, "/v1/auth/passkey/begin", Public, s.passkeyLoginBegin)
		s.handle(api, http.MethodPost, "/v1/auth/passkey/finish", Public, s.passkeyLoginFinish)
		s.handle(api, http.MethodPost, "/v1/auth/step-up", Streamer, s.stepUp)
		s.handle(api, http.MethodPost, "/v1/auth/step-up/passkey/begin", Streamer, s.stepUpPasskeyBegin)
		s.handle(api, http.MethodPost, "/v1/auth/step-up/passkey/finish", Streamer, s.stepUpPasskeyFinish)
		// Everyone's own account.
		s.handle(api, http.MethodGet, "/v1/account", Streamer, s.accountGet)
		s.handle(api, http.MethodPost, "/v1/account/password", Streamer, s.accountPassword)
		s.handle(api, http.MethodGet, "/v1/account/sessions", Streamer, s.accountSessions)
		s.handle(api, http.MethodDelete, "/v1/account/sessions/{handle}", Streamer, s.accountSessionEnd)
		s.handle(api, http.MethodPost, "/v1/account/totp/setup", Streamer, s.totpSetup)
		s.handle(api, http.MethodPost, "/v1/account/totp/enable", Streamer, s.totpEnable)
		s.handle(api, http.MethodPost, "/v1/account/totp/disable", Streamer, s.totpDisable)
		s.handle(api, http.MethodPost, "/v1/account/recovery-codes", Streamer, s.recoveryCodes)
		s.handle(api, http.MethodPost, "/v1/account/passkeys/begin", Streamer, s.passkeyRegisterBegin)
		s.handle(api, http.MethodPost, "/v1/account/passkeys/finish", Streamer, s.passkeyRegisterFinish)
		s.handle(api, http.MethodPatch, "/v1/account/passkeys/{pid}", Streamer, s.passkeyRename)
		s.handle(api, http.MethodDelete, "/v1/account/passkeys/{pid}", Streamer, s.passkeyDelete)
		s.handleStepUp(api, http.MethodPut, "/v1/account/step-up", Streamer, s.accountStepUp)
		s.handle(api, http.MethodPost, "/v1/auth/logout", Streamer, s.logout)
		s.handle(api, http.MethodGet, "/v1/session", Streamer, s.session)
		s.handle(api, http.MethodPost, "/v1/join", Public, s.join)
		// A public stream's watch link: anyone, no session. WebRTC goes through /whep/<path>.
		s.handle(api, http.MethodGet, "/v1/public/streams/*", Public, s.publicStreamGet)
		s.handle(api, http.MethodGet, "/v1/public/hls/*", Public, s.publicHLS)
		s.handle(api, http.MethodGet, "/v1/status", Viewer, s.status)
		s.handle(api, http.MethodGet, "/v1/events", Streamer, s.events) // a streamer's is scoped to its streams
		s.handle(api, http.MethodGet, "/v1/metrics/history", Viewer, s.history)

		// Watching: streamers only their own streams (mayWatch); a WHEP session belongs to whoever started it.
		s.handle(api, http.MethodGet, "/v1/live/hls/*", Streamer, s.watchHLS)
		s.handle(api, http.MethodPost, "/v1/live/whep/*", Streamer, s.watchWHEP)
		s.handle(api, http.MethodPatch, "/v1/live/whep-session/{id}", Streamer, s.watchWHEPSession)
		s.handle(api, http.MethodDelete, "/v1/live/whep-session/{id}", Streamer, s.watchWHEPSession)
		s.handle(api, http.MethodGet, "/v1/recordings", Viewer, s.recordingsList)
		s.handle(api, http.MethodGet, "/v1/recordings/spans", Viewer, s.recordingSpans)
		s.handle(api, http.MethodGet, "/v1/recordings/segments", Viewer, s.recordingSegments)
		s.handle(api, http.MethodGet, "/v1/recordings/export", Viewer, s.recordingExport)
		s.handle(api, http.MethodPost, "/v1/recordings/delete", Operator, s.recordingsDelete)
		s.handle(api, http.MethodPost, "/v1/recordings/guard/release", Admin, s.recordingsGuardRelease)
		s.handle(api, http.MethodGet, "/v1/layouts", Streamer, s.layoutsList)
		s.handle(api, http.MethodPut, "/v1/layouts/{name}", Streamer, s.layoutSave)
		s.handle(api, http.MethodDelete, "/v1/layouts/{name}", Streamer, s.layoutDelete)
		s.handle(api, http.MethodGet, "/v1/watch/current", Streamer, s.watchCurrentGet)
		s.handle(api, http.MethodPut, "/v1/watch/current", Streamer, s.watchCurrentPut)
		s.handle(api, http.MethodGet, "/v1/streams", Streamer, s.streamsList)
		s.handle(api, http.MethodPost, "/v1/streams", Admin, s.streamCreate)
		s.handle(api, http.MethodGet, "/v1/streams/{id}", Streamer, s.streamGet)
		s.handle(api, http.MethodPatch, "/v1/streams/{id}", Streamer, s.streamPatch)
		s.handle(api, http.MethodDelete, "/v1/streams/{id}", Admin, s.streamDelete)
		s.handle(api, http.MethodGet, "/v1/streams/{id}/history", Streamer, s.streamHistory)
		s.handle(api, http.MethodPost, "/v1/streams/{id}/keys/{kind}/reveal", Streamer, s.streamKeyReveal)
		s.handle(api, http.MethodPost, "/v1/streams/{id}/keys/{kind}/regenerate", Streamer, s.streamKeyRegenerate)
		s.handle(api, http.MethodPost, "/v1/streams/{id}/disconnect", Streamer, s.streamDisconnect)
		s.handle(api, http.MethodPost, "/v1/streams/{id}/lease", Streamer, s.streamLease)
		s.handle(api, http.MethodPut, "/v1/streams/{id}/holding/clip", Streamer, s.holdingUpload)
		s.handle(api, http.MethodGet, "/v1/streams/{id}/notes", Streamer, s.streamNotes)
		s.handle(api, http.MethodGet, "/v1/streams/{id}/guest-keys", Streamer, s.guestKeysList)
		s.handle(api, http.MethodPost, "/v1/streams/{id}/guest-keys", Streamer, s.guestKeyCreate)
		s.handle(api, http.MethodDelete, "/v1/streams/{id}/guest-keys/{kid}", Streamer, s.guestKeyRevoke)
		s.handle(api, http.MethodGet, "/v1/streams/{id}/forwards", Streamer, s.forwardsList)
		s.handle(api, http.MethodPost, "/v1/streams/{id}/forwards", Streamer, s.forwardCreate)
		s.handle(api, http.MethodPatch, "/v1/streams/{id}/forwards/{fid}", Streamer, s.forwardPatch)
		s.handle(api, http.MethodDelete, "/v1/streams/{id}/forwards/{fid}", Streamer, s.forwardDelete)
		s.handle(api, http.MethodGet, "/v1/users", Admin, s.usersList)
		s.handleStepUp(api, http.MethodPost, "/v1/users", Admin, s.userInvite)
		s.handleStepUp(api, http.MethodPost, "/v1/users/{id}/join-code", Admin, s.userJoinCode)
		s.handleStepUp(api, http.MethodPatch, "/v1/users/{id}", Admin, s.userPatch)
		s.handleStepUp(api, http.MethodDelete, "/v1/users/{id}", Admin, s.userDelete)
		s.handle(api, http.MethodGet, "/v1/users/{id}/sessions", Admin, s.userSessions)
		s.handleStepUp(api, http.MethodDelete, "/v1/users/{id}/sessions", Admin, s.userSessionsEnd)
		s.handleStepUp(api, http.MethodDelete, "/v1/users/{id}/second-factor", Admin, s.userSecondFactorReset)
		s.handle(api, http.MethodGet, "/v1/backups", Admin, s.backupsList)
		s.handle(api, http.MethodPost, "/v1/backups", Admin, s.backupCreate)
		s.handleStepUp(api, http.MethodPut, "/v1/backups/passphrase", Admin, s.backupPassphrase)
		s.handleStepUp(api, http.MethodPut, "/v1/backups/schedule", Admin, s.backupSchedule)
		s.handle(api, http.MethodPost, "/v1/backups/upload", Admin, s.backupUpload)
		s.handle(api, http.MethodDelete, "/v1/backups/restore", Admin, s.backupCancel)
		s.handle(api, http.MethodGet, "/v1/backups/{name}/download", Admin, s.backupDownload)
		s.handleStepUp(api, http.MethodDelete, "/v1/backups/{name}", Admin, s.backupDelete)
		s.handle(api, http.MethodPost, "/v1/backups/{name}/check", Admin, s.backupCheck)
		s.handleStepUp(api, http.MethodPost, "/v1/backups/{name}/restore", Admin, s.backupRestore)
		s.handle(api, http.MethodGet, "/v1/logs", Admin, s.logsSearch)
		s.handle(api, http.MethodGet, "/v1/logs/stream", Admin, s.logsStream)
		s.handle(api, http.MethodGet, "/v1/logs/download", Admin, s.logsDownload)
		s.handle(api, http.MethodGet, "/v1/audit", Admin, s.auditList)
		s.handle(api, http.MethodGet, "/v1/audit/export", Admin, s.auditExport)
		s.handle(api, http.MethodGet, "/v1/credentials", Admin, s.credentialsList)
		s.handleStepUp(api, http.MethodPost, "/v1/credentials", Admin, s.credentialCreate)
		s.handleStepUp(api, http.MethodPost, "/v1/credentials/{name}/revoke", Admin, s.credentialRevoke)
		s.handle(api, http.MethodGet, "/v1/exposure", Admin, s.exposureOnly(s.exposureGet))
		s.handleStepUp(api, http.MethodPut, "/v1/exposure/{id}", Admin, s.exposureOnly(s.exposureOpen))
		s.handleStepUp(api, http.MethodDelete, "/v1/exposure/{id}", Admin, s.exposureOnly(s.exposureClose))
		s.handleStepUp(api, http.MethodPost, "/v1/exposure/close-all", Admin, s.exposureOnly(s.exposureCloseAll))
		s.handleStepUp(api, http.MethodPut, "/v1/exposure/auto", Admin, s.exposureOnly(s.exposureAutoPut))
		s.handle(api, http.MethodGet, "/v1/config", Admin, s.configGet)
		s.handleStepUp(api, http.MethodPut, "/v1/config", Admin, s.configReplace)
		s.handle(api, http.MethodPost, "/v1/config/validate", Admin, s.configValidate)
		s.handle(api, http.MethodPost, "/v1/config/drift/dismiss", Admin, s.driftDismiss)
		s.handle(api, http.MethodGet, "/v1/config/snapshots", Admin, s.snapshotsList)
		s.handle(api, http.MethodGet, "/v1/config/snapshots/{id}", Admin, s.snapshotGet)
		s.handleStepUp(api, http.MethodPost, "/v1/config/snapshots/{id}/restore", Admin, s.snapshotRestore)
		// The structured edits stay without step-up on purpose (D12: step-up only for the YAML editor, restores and the
		// other admin-level actions, so everyday settings do not keep asking); every edit is validated and audited.
		s.handle(api, http.MethodPatch, "/v1/config/global", Admin, s.globalPatch)
		s.handle(api, http.MethodPatch, "/v1/config/path-defaults", Admin, s.pathDefaultsPatch)
		s.handle(api, http.MethodPut, "/v1/config/paths/*", Admin, s.pathPut)
		s.handle(api, http.MethodDelete, "/v1/config/paths/*", Admin, s.pathDelete)

		s.routes = append(s.routes, Route{"*", "/api/mtx/*", PerOperation, false})
		api.With(s.require(PerOperation)).Mount("/mtx", http.StripPrefix("/api/mtx", s.d.Proxy))
	})

	// WHIP and WHEP for clients outside the UI; MediaMTX authenticates them with their stream credential.
	for _, kind := range []string{"whip", "whep"} {
		s.routes = append(s.routes, Route{http.MethodPost, "/" + kind + "/*", Public, false})
		r.Post("/"+kind+"/*", s.externalOffer(kind))
	}
	for _, m := range []string{http.MethodPatch, http.MethodDelete} {
		s.routes = append(s.routes, Route{m, "/rtc-session/{id}", Public, false})
		r.Method(m, "/rtc-session/{id}", http.HandlerFunc(s.externalSession))
	}

	s.routes = append(s.routes, Route{"*", "/*", Public, false})
	r.Handle("/*", s.d.UI)
	return r
}

// handle registers a route under /api with its access level.
func (s *Server) handle(r chi.Router, method, pattern string, access Access, h http.HandlerFunc) {
	s.routes = append(s.routes, Route{method, "/api" + pattern, access, false})
	r.With(s.require(access)).Method(method, pattern, h)
}

// handleStepUp registers an admin-level change: besides the access level, the session must have proved who it is
// within the last hour (unless the user opted out).
func (s *Server) handleStepUp(r chi.Router, method, pattern string, access Access, h http.HandlerFunc) {
	s.routes = append(s.routes, Route{method, "/api" + pattern, access, true})
	r.With(s.require(access), s.requireStepUp).Method(method, pattern, h)
}

// require enforces an access level.
func (s *Server) require(access Access) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if access == Public {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cur, ok := current(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in first.")
				return
			}
			if access != PerOperation && !auth.Role(cur.user.Role).AtLeast(auth.Role(access)) {
				writeError(w, http.StatusForbidden, "forbidden", "This needs the "+string(access)+" role.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Internal is the handler for the internal listener.
func (s *Server) Internal() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	r.With(s.fromMediaMTX).Post("/internal/auth", s.d.MTXAuth.ServeHTTP)
	return r
}
