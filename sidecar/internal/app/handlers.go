package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"time"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/buildinfo"
	"mtxui/internal/probe"
	"mtxui/internal/setup"
	"mtxui/internal/store"
)

// Health is the body of GET /api/v1/health.
type Health struct {
	Status          string `json:"status"`
	Version         string `json:"version"`
	MediaMTXVersion string `json:"mediamtxVersion"`
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{Status: "ok", Version: buildinfo.Version, MediaMTXVersion: buildinfo.MediaMTXVersion})
}

// decodeJSON reads a small JSON body. It insists on the JSON content type, which a cross-site HTML form cannot send.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONLimit(w, r, v, 16<<10)
}

// decodeJSONLimit is decodeJSON for bodies up to limit bytes.
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send JSON.")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "The request body is not the expected JSON.")
		return false
	}
	return true
}

func retryAfter(w http.ResponseWriter, d time.Duration) int {
	secs := int(math.Ceil(d.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	return secs
}

// SessionInfo describes the signed-in session to the UI.
type SessionInfo struct {
	User       UserInfo  `json:"user"`
	CSRFToken  string    `json:"csrfToken"`
	AuthMethod string    `json:"authMethod"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Warning    string    `json:"warning,omitempty"`
}

// UserInfo is the public part of a user.
type UserInfo struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func sessionInfo(sess store.Session, u store.User) SessionInfo {
	return SessionInfo{User: UserInfo{u.ID, u.Username, u.Role}, CSRFToken: sess.CSRFToken, AuthMethod: sess.AuthMethod, ExpiresAt: sess.ExpiresAt}
}

// startSession signs user in: any session the browser already had ends (no fixation), and a new one begins.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user store.User, method string) (store.Session, error) {
	if c, ok := current(r.Context()); ok {
		_ = s.d.Sessions.Destroy(r.Context(), c.token)
	}
	token, sess, err := s.d.Sessions.Create(r.Context(), user, method, clientip.From(r.Context()).IP.String(), r.UserAgent())
	if err != nil {
		return store.Session{}, err
	}
	s.d.Sessions.SetCookie(w, token, sess.ExpiresAt)
	return sess, nil
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	required, err := s.d.Setup.Required(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the setup state.")
		return
	}
	// Passkeys: whether the sign-in page offers "Sign in with a passkey".
	writeJSON(w, http.StatusOK, map[string]bool{"required": required, "passkeys": s.d.Settings.Passkeys})
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if required, err := s.d.Setup.Required(ctx); err != nil || !required {
		writeError(w, http.StatusNotFound, "not_found", "Setup has already been completed.")
		return
	}
	if ok, wait := s.setupRate.Allow(clientip.RateKey(clientip.From(ctx).IP)); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("Too many attempts. Try again in %d seconds.", retryAfter(w, wait)))
		return
	}
	var body struct {
		Token    string       `json:"token"`
		Username string       `json:"username"`
		Password string       `json:"password"`
		Ingest   setup.Ingest `json:"ingest"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	audit.Set(ctx, "setup.complete", body.Username, map[string]any{"ingest": body.Ingest})
	audit.Actor(ctx, body.Username, nil)
	if !s.d.Setup.CheckToken(body.Token) {
		audit.Set(ctx, "", "", map[string]any{"result": "wrong token"})
		writeError(w, http.StatusUnauthorized, "invalid_token", "The setup token is wrong. The sidecar prints it in its log, and it is in state/setup-token.")
		return
	}
	user, warning, err := s.d.Setup.Complete(ctx, setup.Request{Username: body.Username, Password: body.Password, Ingest: body.Ingest})
	var verr setup.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, "invalid", verr.Msg)
		return
	case errors.Is(err, store.ErrSetupDone):
		writeError(w, http.StatusNotFound, "not_found", "Setup has already been completed.")
		return
	case err != nil:
		s.d.Log.Error("setup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Setup failed: "+err.Error())
		return
	}
	audit.Actor(ctx, user.Username, &user.ID)
	audit.Set(ctx, "", "", map[string]any{"result": "ok"})
	if warning != "" {
		s.d.Log.Warn(warning)
	}
	sess, err := s.startSession(w, r, user, "password")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Setup is complete, but signing in failed. Sign in again.")
		return
	}
	info := sessionInfo(sess, user)
	info.Warning = warning
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	audit.Set(ctx, "auth.login", body.Username, nil)
	audit.Actor(ctx, body.Username, nil)
	if ok, wait := s.loginRate.Allow(clientip.RateKey(clientip.From(ctx).IP)); !ok {
		audit.Set(ctx, "", "", map[string]any{"result": "rate limited"})
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("Too many sign-in attempts from your address. Try again in %d seconds.", retryAfter(w, wait)))
		return
	}
	key := auth.LockKey(body.Username)
	if locked, wait := s.lockout.Locked(key); locked {
		audit.Set(ctx, "", "", map[string]any{"result": "locked"})
		writeError(w, http.StatusTooManyRequests, "locked",
			fmt.Sprintf("Too many failed sign-ins for this username. Try again in %d minutes.", int(math.Ceil(float64(retryAfter(w, wait))/60))))
		return
	}

	// Exactly one argon2id verification on every path below, so unknown usernames take as long as wrong passwords.
	user, err := s.d.Store.UserByName(ctx, body.Username)
	ok := false
	switch {
	// An invited account that has not joined yet has no password: it cannot sign in, and takes as long as any other.
	case errors.Is(err, store.ErrNotFound), err == nil && user.PasswordHash == "":
		if err := s.d.Hasher.VerifyDummy(ctx, body.Password); err != nil && ctx.Err() != nil {
			writeError(w, http.StatusServiceUnavailable, "busy", "The server is busy. Try again.")
			return
		}
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the user database.")
		return
	default:
		good, rehash, verr := s.d.Hasher.Verify(ctx, user.PasswordHash, body.Password)
		if verr != nil && ctx.Err() != nil {
			writeError(w, http.StatusServiceUnavailable, "busy", "The server is busy. Try again.")
			return
		}
		if verr != nil {
			s.d.Log.Error("stored password hash is unusable", "user", user.Username, "err", verr)
		}
		ok = good && !user.Disabled
		if ok && rehash {
			if h, err := s.d.Hasher.Hash(ctx, body.Password); err == nil {
				_ = s.d.Store.UpdatePasswordHash(ctx, user.ID, h)
			}
		}
	}
	if !ok {
		if s.lockout.Fail(key) {
			s.d.Log.Warn("username locked after repeated failed sign-ins", "username", body.Username)
		}
		audit.Set(ctx, "", "", map[string]any{"result": "failed"})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Wrong username or password.")
		return
	}
	s.lockout.Succeed(key)
	if user.TOTPEnc != "" {
		// The authenticator app's code comes next (POST /v1/auth/login/code): no session before it.
		ticket, err := s.newTicket(user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "Cannot start the sign-in.")
			return
		}
		audit.Actor(ctx, user.Username, &user.ID)
		audit.Set(ctx, "", "", map[string]any{"result": "second factor"})
		writeJSON(w, http.StatusOK, map[string]any{"secondFactor": "totp", "ticket": ticket})
		return
	}
	sess, err := s.startSession(w, r, user, "password")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start a session.")
		return
	}
	audit.Actor(ctx, user.Username, &user.ID)
	audit.Set(ctx, "", "", map[string]any{"result": "ok"})
	writeJSON(w, http.StatusOK, sessionInfo(sess, user))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := current(r.Context())
	audit.Set(r.Context(), "auth.logout", c.user.Username, nil)
	if err := s.d.Sessions.Destroy(r.Context(), c.token); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot end the session.")
		return
	}
	s.d.Sessions.ClearCookie(w)
	s.sessionNudge.fire() // this session's open event streams end now, not at their next recheck
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	c, _ := current(r.Context())
	writeJSON(w, http.StatusOK, sessionInfo(c.session, c.user))
}

// StatusView is the status endpoint's answer: MediaMTX's state and warnings, and which optional features are on.
type StatusView struct {
	probe.Status
	ExposureControl bool `json:"exposureControl"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st := s.d.Probe.Status()
	st.Warnings = append(st.Warnings, s.mismatchWarnings(time.Now())...)
	st.Warnings = append(st.Warnings, s.recordingWarnings(r.Context())...)
	sort.Slice(st.Warnings, func(i, j int) bool { return st.Warnings[i].Code < st.Warnings[j].Code })
	writeJSON(w, http.StatusOK, StatusView{Status: st, ExposureControl: s.d.Settings.ExposureControl})
}
