package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/store"
)

// Second factors and step-up: an authenticator app (TOTP, with single-use
// recovery codes) and passkeys are optional for everyone. With TOTP on, signing in with a password takes its code as
// a second step; a passkey signs in alone (it is two factors in one). Admin-level changes ask the session to prove
// who it is again when it last did more than an hour ago (step-up), unless the user opted out; signing in counts.

// stepUpWindow is how long a session's proof lasts for admin-level changes.
const stepUpWindow = time.Hour

const (
	ticketTTL    = 5 * time.Minute // between the password and the code
	ticketTries  = 5               // codes per ticket
	ceremonyTTL  = 5 * time.Minute // a passkey prompt
	totpSetupTTL = 15 * time.Minute
)

// authFlows holds what spans requests of a sign-in or a setup, in memory: second-step tickets, passkey ceremonies,
// TOTP secrets waiting for their first code.
type authFlows struct {
	mu         sync.Mutex
	tickets    map[string]*ticket
	ceremonies map[string]ceremony
	totpSetups map[int64]totpSetup
	once       sync.Once
	wa         *webauthn.WebAuthn
	waErr      error
}

type ticket struct {
	userID  int64
	expires time.Time
	tries   int
}

type ceremony struct {
	purpose string // "login", "register", "step-up"
	userID  int64  // 0 for a passkey sign-in (the passkey names the user)
	data    webauthn.SessionData
	expires time.Time
}

type totpSetup struct {
	secret  string
	expires time.Time
}

func (f *authFlows) init() {
	if f.tickets == nil {
		f.tickets, f.ceremonies, f.totpSetups = map[string]*ticket{}, map[string]ceremony{}, map[int64]totpSetup{}
	}
}

// sweep forgets what has expired (called under the lock).
func (f *authFlows) sweep(now time.Time) {
	for k, t := range f.tickets {
		if now.After(t.expires) {
			delete(f.tickets, k)
		}
	}
	for k, c := range f.ceremonies {
		if now.After(c.expires) {
			delete(f.ceremonies, k)
		}
	}
	for k, t := range f.totpSetups {
		if now.After(t.expires) {
			delete(f.totpSetups, k)
		}
	}
}

func newFlowID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) putCeremony(c ceremony) (string, error) {
	id, err := newFlowID()
	if err != nil {
		return "", err
	}
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	s.flows.init()
	s.flows.sweep(time.Now())
	c.expires = time.Now().Add(ceremonyTTL)
	s.flows.ceremonies[id] = c
	return id, nil
}

// takeCeremony returns a ceremony once.
func (s *Server) takeCeremony(id, purpose string, userID int64) (ceremony, bool) {
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	s.flows.init()
	c, ok := s.flows.ceremonies[id]
	delete(s.flows.ceremonies, id)
	if !ok || c.purpose != purpose || c.userID != userID || time.Now().After(c.expires) {
		return ceremony{}, false
	}
	return c, true
}

// webAuthn is the relying party: the host of MTXUI_PUBLIC_URL, its origin.
func (s *Server) webAuthn() (*webauthn.WebAuthn, error) {
	if !s.d.Settings.Passkeys {
		return nil, errors.New("passkeys are not available here")
	}
	s.flows.once.Do(func() {
		s.flows.wa, s.flows.waErr = webauthn.New(&webauthn.Config{
			RPID: s.d.Settings.PublicURL.Hostname(), RPDisplayName: "MediaMTX UI", RPOrigins: []string{s.d.Settings.Origin()},
		})
	})
	return s.flows.wa, s.flows.waErr
}

// waUser is a user as the WebAuthn library sees it, with their passkeys.
type waUser struct {
	u      store.User
	handle []byte
	keys   []store.Passkey
	creds  []webauthn.Credential
}

func (w *waUser) WebAuthnID() []byte                         { return w.handle }
func (w *waUser) WebAuthnName() string                       { return w.u.Username }
func (w *waUser) WebAuthnDisplayName() string                { return w.u.Username }
func (w *waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func (s *Server) loadWAUser(ctx context.Context, u store.User) (*waUser, error) {
	handle, err := s.d.Store.WebAuthnID(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	keys, err := s.d.Store.Passkeys(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	w := &waUser{u: u, handle: handle, keys: keys}
	for _, k := range keys {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(k.Credential), &c); err != nil {
			return nil, fmt.Errorf("passkey %d: %w", k.ID, err)
		}
		w.creds = append(w.creds, c)
	}
	return w, nil
}

// passkeyUsed checks a passkey's assertion result and stores its moved-on counter. A counter that did not move on
// means a cloned authenticator: refused.
func (s *Server) passkeyUsed(ctx context.Context, w *waUser, c *webauthn.Credential) error {
	if c.Authenticator.CloneWarning {
		return errors.New("this passkey's signature counter went backwards, a sign of a copied authenticator")
	}
	for _, k := range w.keys {
		if string(k.CredentialID) == string(c.ID) {
			b, err := json.Marshal(c)
			if err != nil {
				return err
			}
			return s.d.Store.PasskeyUsed(ctx, k.ID, string(b))
		}
	}
	return errors.New("unknown passkey")
}

// checkCode verifies a TOTP code or a recovery code of a user with TOTP on, once: it returns how ("totp",
// "recovery") or "" when the code is wrong or used.
func (s *Server) checkCode(ctx context.Context, u store.User, code string) (string, error) {
	if u.TOTPEnc == "" {
		return "", nil
	}
	norm := strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(norm) == 6 {
		secret, err := s.d.Creds.Unseal(u.TOTPEnc, totpSealContext(u.ID))
		if err != nil {
			return "", err
		}
		step, ok := auth.TOTPMatch(secret, norm, time.Now())
		if !ok {
			return "", nil
		}
		fresh, err := s.d.Store.UseTOTPStep(ctx, u.ID, step)
		if err != nil || !fresh {
			return "", err // a replayed code
		}
		return "totp", nil
	}
	used, err := s.d.Store.UseRecoveryCode(ctx, u.ID, auth.RecoveryHash(code))
	if err != nil || !used {
		return "", err
	}
	return "recovery", nil
}

func totpSealContext(userID int64) string { return fmt.Sprintf("user %d totp", userID) }

// newTicket starts the second step of a password sign-in.
func (s *Server) newTicket(userID int64) (string, error) {
	id, err := newFlowID()
	if err != nil {
		return "", err
	}
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	s.flows.init()
	s.flows.sweep(time.Now())
	s.flows.tickets[id] = &ticket{userID: userID, expires: time.Now().Add(ticketTTL)}
	return id, nil
}

// loginCode is a password sign-in's second step: the TOTP code, or a recovery code.
func (s *Server) loginCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	audit.Set(ctx, "auth.login", "", nil)
	if ok, wait := s.loginRate.Allow(clientip.RateKey(clientip.From(ctx).IP)); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("Too many sign-in attempts from your address. Try again in %d seconds.", retryAfter(w, wait)))
		return
	}
	s.flows.mu.Lock()
	s.flows.init()
	t, ok := s.flows.tickets[body.Ticket]
	if ok {
		t.tries++
		if t.tries > ticketTries || time.Now().After(t.expires) {
			delete(s.flows.tickets, body.Ticket)
			ok = false
		}
	}
	var userID int64
	if ok {
		userID = t.userID
	}
	s.flows.mu.Unlock()
	if !ok {
		writeError(w, http.StatusUnauthorized, "ticket", "This sign-in has expired. Enter your password again.")
		return
	}
	u, err := s.d.Store.UserByID(ctx, userID)
	if err != nil || u.Disabled {
		writeError(w, http.StatusUnauthorized, "ticket", "This sign-in has expired. Enter your password again.")
		return
	}
	audit.Set(ctx, "", u.Username, nil)
	audit.Actor(ctx, u.Username, &u.ID)
	// Wrong codes count per account (not only per ticket): whoever has the password cannot get fresh tickets to
	// keep guessing codes.
	attempt, wait := s.lockout.Begin(auth.AccountKey(u.ID))
	if attempt == nil {
		writeError(w, http.StatusTooManyRequests, "locked", fmt.Sprintf("Too many wrong codes. Try again in %d minutes.", (retryAfter(w, wait)+59)/60))
		return
	}
	defer attempt.Abandon()
	how, err := s.checkCode(ctx, u, body.Code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The code cannot be checked.")
		return
	}
	if how == "" {
		attempt.Fail()
		audit.Set(ctx, "", "", map[string]any{"result": "wrong code"})
		writeError(w, http.StatusUnauthorized, "invalid_code", "Wrong or already used code.")
		return
	}
	attempt.Succeed()
	s.flows.mu.Lock()
	delete(s.flows.tickets, body.Ticket)
	s.flows.mu.Unlock()
	sess, err := s.startSession(w, r, u, "password+"+how)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start a session.")
		return
	}
	audit.Set(ctx, "", "", map[string]any{"result": "ok", "with": how})
	writeJSON(w, http.StatusOK, sessionInfo(sess, u))
}

// Passkey sign-in: discoverable credentials, so no username is needed.

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, _ *http.Request) {
	wa, err := s.webAuthn()
	if err != nil {
		writeError(w, http.StatusNotFound, "unavailable", "Passkeys are not available on this server's address.")
		return
	}
	assertion, data, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start a passkey sign-in.")
		return
	}
	id, err := s.putCeremony(ceremony{purpose: "login", data: *data})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start a passkey sign-in.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "options": assertion.Response})
}

type passkeyFinish struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Credential json.RawMessage `json:"credential"`
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body passkeyFinish
	if !decodeJSONLimit(w, r, &body, 64<<10) {
		return
	}
	audit.Set(ctx, "auth.login", "", map[string]any{"with": "passkey"})
	if ok, wait := s.loginRate.Allow(clientip.RateKey(clientip.From(ctx).IP)); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			fmt.Sprintf("Too many sign-in attempts from your address. Try again in %d seconds.", retryAfter(w, wait)))
		return
	}
	wa, err := s.webAuthn()
	c, ok := s.takeCeremony(body.ID, "login", 0)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "passkey", "The passkey prompt expired. Try again.")
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body.Credential)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey", "The passkey's answer cannot be read.")
		return
	}
	var who *waUser
	user, cred, err := wa.ValidatePasskeyLogin(func(_, handle []byte) (webauthn.User, error) {
		u, err := s.d.Store.UserByWebAuthnID(ctx, handle)
		if err != nil {
			return nil, err
		}
		who, err = s.loadWAUser(ctx, u)
		return who, err
	}, c.data, parsed)
	if err == nil && (user == nil || who == nil) {
		err = errors.New("no user")
	}
	if err == nil {
		err = s.passkeyUsed(ctx, who, cred)
	}
	if err == nil && who.u.Disabled {
		err = errors.New("the account is disabled")
	}
	if err != nil {
		audit.Set(ctx, "", "", map[string]any{"result": "failed"})
		s.d.Log.Info("passkey sign-in refused", "err", err)
		writeError(w, http.StatusUnauthorized, "passkey", "This passkey was not accepted.")
		return
	}
	audit.Set(ctx, "", who.u.Username, nil)
	audit.Actor(ctx, who.u.Username, &who.u.ID)
	sess, err := s.startSession(w, r, who.u, "passkey")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start a session.")
		return
	}
	audit.Set(ctx, "", "", map[string]any{"result": "ok"})
	writeJSON(w, http.StatusOK, sessionInfo(sess, who.u))
}

// Step-up.

// stepUpDue reports whether the session must prove who it is again before an admin-level change.
func stepUpDue(cur *currentSession, now time.Time) bool {
	return !cur.user.StepUpOptOut && now.Sub(cur.session.VerifiedAt) > stepUpWindow
}

// requireStepUp guards admin-level changes: 403 "step_up" until the session proves who it is again.
func (s *Server) requireStepUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur, ok := current(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in first.")
			return
		}
		if stepUpDue(cur, time.Now()) {
			writeError(w, http.StatusForbidden, "step_up", "Confirm it is you to make this change.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// stepUp proves who the session is with the password or a code.
func (s *Server) stepUp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	audit.Set(ctx, "auth.step_up", cur.user.Username, nil)
	attempt, ok := s.accountAttempt(w, cur.user.ID)
	if !ok {
		return
	}
	defer attempt.Abandon()
	how := ""
	switch {
	case body.Code != "":
		var err error
		if how, err = s.checkCode(ctx, cur.user, body.Code); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "The code cannot be checked.")
			return
		}
	case body.Password != "":
		good, _, err := s.d.Hasher.Verify(ctx, cur.user.PasswordHash, body.Password)
		if hashBusy(ctx, err) {
			writeError(w, http.StatusServiceUnavailable, "busy", "The server is busy. Try again.")
			return
		}
		if good {
			how = "password"
		}
	}
	if how == "" {
		attempt.Fail()
		audit.Set(ctx, "", "", map[string]any{"result": "failed"})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "That is not right.")
		return
	}
	attempt.Succeed()
	s.verified(w, r, cur, how)
}

func (s *Server) verified(w http.ResponseWriter, r *http.Request, cur *currentSession, how string) {
	now := time.Now()
	if err := s.d.Store.SetSessionVerified(r.Context(), cur.session.IDHash, now); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot record it.")
		return
	}
	audit.Set(r.Context(), "", "", map[string]any{"result": "ok", "with": how})
	writeJSON(w, http.StatusOK, map[string]any{"verifiedAt": now})
}

func (s *Server) stepUpPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	wa, err := s.webAuthn()
	if err != nil {
		writeError(w, http.StatusNotFound, "unavailable", "Passkeys are not available on this server's address.")
		return
	}
	who, err := s.loadWAUser(ctx, cur.user)
	if err != nil || len(who.creds) == 0 {
		writeError(w, http.StatusNotFound, "no_passkey", "You have no passkey.")
		return
	}
	assertion, data, err := wa.BeginLogin(who, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start the passkey prompt.")
		return
	}
	id, err := s.putCeremony(ceremony{purpose: "step-up", userID: cur.user.ID, data: *data})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start the passkey prompt.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "options": assertion.Response})
}

func (s *Server) stepUpPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body passkeyFinish
	if !decodeJSONLimit(w, r, &body, 64<<10) {
		return
	}
	audit.Set(ctx, "auth.step_up", cur.user.Username, map[string]any{"with": "passkey"})
	wa, err := s.webAuthn()
	c, ok := s.takeCeremony(body.ID, "step-up", cur.user.ID)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "passkey", "The passkey prompt expired. Try again.")
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body.Credential)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey", "The passkey's answer cannot be read.")
		return
	}
	who, err := s.loadWAUser(ctx, cur.user)
	if err == nil {
		var cred *webauthn.Credential
		if cred, err = wa.ValidateLogin(who, c.data, parsed); err == nil {
			err = s.passkeyUsed(ctx, who, cred)
		}
	}
	if err != nil {
		audit.Set(ctx, "", "", map[string]any{"result": "failed"})
		writeError(w, http.StatusUnauthorized, "passkey", "This passkey was not accepted.")
		return
	}
	s.verified(w, r, cur, "passkey")
}
