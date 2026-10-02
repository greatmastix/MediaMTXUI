package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/store"
)

// The account page's API: every signed-in user, streamers included, manages their own password,
// sessions, authenticator app, recovery codes, passkeys and whether admin-level changes ask them again.

// AccountInfo is the signed-in user's account.
type AccountInfo struct {
	Username          string        `json:"username"`
	Role              string        `json:"role"`
	TOTP              bool          `json:"totp"`
	RecoveryCodesLeft int           `json:"recoveryCodesLeft"`
	Passkeys          []PasskeyInfo `json:"passkeys"`
	PasskeysAvailable bool          `json:"passkeysAvailable"`
	StepUpOptOut      bool          `json:"stepUpOptOut"`
	VerifiedAt        *time.Time    `json:"verifiedAt"`
	StepUpDue         bool          `json:"stepUpDue"` // an admin-level change would ask now
}

// PasskeyInfo describes a passkey; never its key.
type PasskeyInfo struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func (s *Server) accountInfo(r *http.Request) (AccountInfo, error) {
	ctx := r.Context()
	cur, _ := current(ctx)
	u, err := s.d.Store.UserByID(ctx, cur.user.ID)
	if err != nil {
		return AccountInfo{}, err
	}
	left, err := s.d.Store.RecoveryCodesLeft(ctx, u.ID)
	if err != nil {
		return AccountInfo{}, err
	}
	keys, err := s.d.Store.Passkeys(ctx, u.ID)
	if err != nil {
		return AccountInfo{}, err
	}
	info := AccountInfo{
		Username: u.Username, Role: u.Role, TOTP: u.TOTPEnc != "", RecoveryCodesLeft: left, Passkeys: []PasskeyInfo{},
		PasskeysAvailable: s.d.Settings.Passkeys, StepUpOptOut: u.StepUpOptOut,
		StepUpDue: stepUpDue(&currentSession{session: cur.session, user: u}, time.Now()),
	}
	if !cur.session.VerifiedAt.IsZero() {
		v := cur.session.VerifiedAt
		info.VerifiedAt = &v
	}
	for _, k := range keys {
		info.Passkeys = append(info.Passkeys, PasskeyInfo{ID: k.ID, Name: k.Name, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt})
	}
	return info, nil
}

func (s *Server) accountGet(w http.ResponseWriter, r *http.Request) {
	info, err := s.accountInfo(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read your account.")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) writeAccount(w http.ResponseWriter, r *http.Request) {
	s.accountGet(w, r)
}

// accountAttempt reserves a password or code check by a signed-in user: rate-limited and locked per account, apart
// from anonymous sign-ins (whose lockout a stranger can trigger). Refused, it has answered already.
func (s *Server) accountAttempt(w http.ResponseWriter, userID int64) (*auth.Attempt, bool) {
	key := auth.AccountKey(userID)
	if ok, wait := s.loginRate.Allow(key); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("Too many attempts. Try again in %d seconds.", retryAfter(w, wait)))
		return nil, false
	}
	attempt, wait := s.lockout.Begin(key)
	if attempt == nil {
		writeError(w, http.StatusTooManyRequests, "locked", "Too many failed attempts. Try again in "+
			strconv.Itoa((retryAfter(w, wait)+59)/60)+" minutes.")
		return nil, false
	}
	return attempt, true
}

// checkPassword verifies the signed-in user's current password (for changes that need it).
func (s *Server) checkPassword(w http.ResponseWriter, r *http.Request, password string) bool {
	ctx := r.Context()
	cur, _ := current(ctx)
	attempt, ok := s.accountAttempt(w, cur.user.ID)
	if !ok {
		return false
	}
	defer attempt.Abandon()
	good, _, err := s.d.Hasher.Verify(ctx, cur.user.PasswordHash, password)
	if hashBusy(ctx, err) {
		writeError(w, http.StatusServiceUnavailable, "busy", "The server is busy. Try again.")
		return false
	}
	if !good {
		attempt.Fail()
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Your current password is not right.")
		return false
	}
	attempt.Succeed()
	return true
}

// accountPassword changes the password; the other sessions end.
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := auth.ValidatePassword(body.New, cur.user.Username); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if !s.checkPassword(w, r, body.Current) {
		return
	}
	hash, err := s.d.Hasher.Hash(ctx, body.New)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "busy", "The server is busy. Try again.")
		return
	}
	if err := s.d.Store.UpdatePasswordHash(ctx, cur.user.ID, hash); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot save the password.")
		return
	}
	ended, _ := s.d.Store.DeleteUserSessionsExcept(ctx, cur.user.ID, cur.session.IDHash)
	s.sessionNudge.fire()
	audit.Set(ctx, "account.password", cur.user.Username, map[string]any{"endedSessions": ended})
	w.WriteHeader(http.StatusNoContent)
}

// SessionView is one of a user's sessions; Handle names it without being able to stand in for it.
type SessionView struct {
	Handle     string    `json:"handle"`
	Current    bool      `json:"current"`
	Method     string    `json:"method"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
}

// sessionHandle is a short public name for a session: the start of its token's hash.
func sessionHandle(idHash string) string {
	if len(idHash) > 16 {
		return idHash[:16]
	}
	return idHash
}

func (s *Server) sessionViews(r *http.Request, userID int64) ([]SessionView, error) {
	cur, _ := current(r.Context())
	list, err := s.d.Store.UserSessions(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	out := []SessionView{}
	for _, sess := range list {
		if time.Now().After(sess.ExpiresAt) {
			continue
		}
		out = append(out, SessionView{
			Handle: sessionHandle(sess.IDHash), Current: sess.IDHash == cur.session.IDHash, Method: sess.AuthMethod,
			CreatedAt: sess.CreatedAt, LastSeenAt: sess.LastSeenAt, IP: sess.IP, UserAgent: sess.UserAgent,
		})
	}
	return out, nil
}

func (s *Server) accountSessions(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	views, err := s.sessionViews(r, cur.user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot list your sessions.")
		return
	}
	writeJSON(w, http.StatusOK, views)
}

// accountSessionEnd signs one of your sessions out (handle "others": all but this one).
func (s *Server) accountSessionEnd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	handle := chi.URLParam(r, "handle")
	if handle == "others" {
		n, err := s.d.Store.DeleteUserSessionsExcept(ctx, cur.user.ID, cur.session.IDHash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "Cannot sign them out.")
			return
		}
		s.sessionNudge.fire()
		audit.Set(ctx, "account.sessions.end", cur.user.Username, map[string]any{"ended": n})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	list, err := s.d.Store.UserSessions(ctx, cur.user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot list your sessions.")
		return
	}
	for _, sess := range list {
		if sessionHandle(sess.IDHash) == handle && len(handle) == 16 {
			if err := s.d.Store.DeleteSession(ctx, sess.IDHash); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "Cannot sign it out.")
				return
			}
			s.sessionNudge.fire()
			audit.Set(ctx, "account.sessions.end", cur.user.Username, map[string]any{"ended": 1})
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "No such session.")
}

// TOTP.

// totpSetup starts setting up an authenticator app; it takes the password. A new second factor also passes
// step-up, so enrolling one must need more than the session: a stolen cookie must not be able to add its own.
func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	cur, _ := current(r.Context())
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) || !s.checkPassword(w, r, body.Password) {
		return
	}
	if cur.user.TOTPEnc != "" {
		writeError(w, http.StatusConflict, "exists", "Your authenticator app is already set up. Switch it off first to set up another.")
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot make a secret.")
		return
	}
	s.flows.mu.Lock()
	s.flows.init()
	s.flows.totpSetups[cur.user.ID] = totpSetup{secret: secret, expires: time.Now().Add(totpSetupTTL)}
	s.flows.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret, "uri": auth.TOTPURI("MediaMTX UI ("+s.d.Settings.PublicURL.Hostname()+")", cur.user.Username, secret),
	})
}

// newRecoveryCodes makes and stores a user's recovery codes, and returns them (the only time they are shown).
func (s *Server) newRecoveryCodes(r *http.Request, userID int64) ([]string, error) {
	codes, err := auth.NewRecoveryCodes(10)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.RecoveryHash(c)
	}
	return codes, s.d.Store.ReplaceRecoveryCodes(r.Context(), userID, hashes)
}

// totpEnable switches the authenticator app on with its first code, and hands out recovery codes.
func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	s.flows.mu.Lock()
	s.flows.init()
	setup, ok := s.flows.totpSetups[cur.user.ID]
	s.flows.mu.Unlock()
	if !ok || time.Now().After(setup.expires) {
		writeError(w, http.StatusConflict, "expired", "Start the setup again.")
		return
	}
	step, ok := auth.TOTPMatch(setup.secret, body.Code, time.Now())
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_code", "That code does not match. Check your phone's clock, and try the newest code.")
		return
	}
	sealed, err := s.d.Creds.Seal(setup.secret, totpSealContext(cur.user.ID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot store the secret.")
		return
	}
	if err := s.d.Store.SetTOTP(ctx, cur.user.ID, sealed); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot store the secret.")
		return
	}
	_, _ = s.d.Store.UseTOTPStep(ctx, cur.user.ID, step) // the setup code does not sign in later
	codes, err := s.newRecoveryCodes(r, cur.user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The app is on, but recovery codes could not be made. Make new ones.")
		return
	}
	s.flows.mu.Lock()
	delete(s.flows.totpSetups, cur.user.ID)
	s.flows.mu.Unlock()
	audit.Set(ctx, "account.totp.enable", cur.user.Username, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// totpDisable switches the authenticator app off (its recovery codes go too); it takes the password.
func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) || !s.checkPassword(w, r, body.Password) {
		return
	}
	if err := s.d.Store.SetTOTP(ctx, cur.user.ID, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot switch it off.")
		return
	}
	_ = s.d.Store.ReplaceRecoveryCodes(ctx, cur.user.ID, nil)
	audit.Set(ctx, "account.totp.disable", cur.user.Username, nil)
	s.writeAccount(w, r)
}

// recoveryCodes replaces the recovery codes; it takes the password.
func (s *Server) recoveryCodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) || !s.checkPassword(w, r, body.Password) {
		return
	}
	if cur.user.TOTPEnc == "" {
		writeError(w, http.StatusConflict, "no_totp", "Recovery codes come with the authenticator app: set it up first.")
		return
	}
	codes, err := s.newRecoveryCodes(r, cur.user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot make recovery codes.")
		return
	}
	audit.Set(ctx, "account.recovery_codes", cur.user.Username, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// Passkeys.

// maxPasskeys is how many passkeys one person may have; checked at the start and, atomically, when one is stored.
const maxPasskeys = 10

// passkeyRegisterBegin starts adding a passkey; it takes the password (see totpSetup: a passkey signs in and passes
// step-up on its own).
func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) || !s.checkPassword(w, r, body.Password) {
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		writeError(w, http.StatusNotFound, "unavailable", "Passkeys are not available on this server's address.")
		return
	}
	who, err := s.loadWAUser(ctx, cur.user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read your passkeys.")
		return
	}
	if len(who.creds) >= maxPasskeys {
		writeError(w, http.StatusConflict, "limit", fmt.Sprintf("You have %d passkeys; remove one first.", maxPasskeys))
		return
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(who.creds))
	for i := range who.creds {
		exclude = append(exclude, who.creds[i].Descriptor())
	}
	required := true
	creation, data, err := wa.BeginRegistration(who, webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: &required,
			UserVerification: protocol.VerificationRequired,
		}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start the passkey setup.")
		return
	}
	id, err := s.putCeremony(ceremony{purpose: "register", userID: cur.user.ID, data: *data})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot start the passkey setup.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "options": creation.Response})
}

func validPasskeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	if utf8.RuneCountInString(name) > 60 || strings.ContainsAny(name, "\x00\n\r") {
		return "", errors.New("a passkey's name is up to 60 characters on one line")
	}
	return name, nil
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body passkeyFinish
	if !decodeJSONLimit(w, r, &body, 64<<10) {
		return
	}
	name, err := validPasskeyName(body.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	wa, err := s.webAuthn()
	c, ok := s.takeCeremony(body.ID, "register", cur.user.ID)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "passkey", "The passkey prompt expired. Try again.")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body.Credential)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey", "The passkey's answer cannot be read.")
		return
	}
	who, err := s.loadWAUser(ctx, cur.user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read your passkeys.")
		return
	}
	cred, err := wa.CreateCredential(who, c.data, parsed)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey", "The passkey was not accepted.")
		return
	}
	b, err := json.Marshal(cred)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot store the passkey.")
		return
	}
	_, err = s.d.Store.AddPasskey(ctx, store.Passkey{UserID: cur.user.ID, CredentialID: cred.ID, Credential: string(b), Name: name}, maxPasskeys)
	if errors.Is(err, store.ErrLimit) {
		writeError(w, http.StatusConflict, "limit", fmt.Sprintf("You have %d passkeys; remove one first.", maxPasskeys))
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "exists", "This passkey is already registered.")
		return
	}
	audit.Set(ctx, "account.passkey.add", cur.user.Username, map[string]any{"name": name})
	s.writeAccount(w, r)
}

func passkeyID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "pid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such passkey.")
		return 0, false
	}
	return id, true
}

func (s *Server) passkeyRename(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	id, ok := passkeyID(w, r)
	var body struct {
		Name string `json:"name"`
	}
	if !ok || !decodeJSON(w, r, &body) {
		return
	}
	name, err := validPasskeyName(body.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := s.d.Store.RenamePasskey(ctx, cur.user.ID, id, name); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such passkey.")
		return
	}
	audit.Set(ctx, "account.passkey.rename", cur.user.Username, map[string]any{"name": name})
	s.writeAccount(w, r)
}

// passkeyDelete removes a passkey. The password always remains, so the last one can go without locking anyone out.
func (s *Server) passkeyDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	id, ok := passkeyID(w, r)
	if !ok {
		return
	}
	if err := s.d.Store.DeletePasskey(ctx, cur.user.ID, id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such passkey.")
		return
	}
	audit.Set(ctx, "account.passkey.remove", cur.user.Username, nil)
	s.writeAccount(w, r)
}

// accountStepUp sets whether admin-level changes ask again (switching it off is itself guarded by step-up).
func (s *Server) accountStepUp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := current(ctx)
	var body struct {
		OptOut bool `json:"optOut"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.d.Store.SetStepUpOptOut(ctx, cur.user.ID, body.OptOut); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot save it.")
		return
	}
	audit.Set(ctx, "account.step_up", cur.user.Username, map[string]any{"optOut": body.OptOut})
	s.writeAccount(w, r)
}
