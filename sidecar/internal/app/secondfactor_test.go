package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"mtxui/internal/auth"
	"mtxui/internal/store"
)

// adminPassword is the password completeSetup gives the admin.
const adminPassword = "correct horse battery"

func (h *harness) me() store.User {
	h.t.Helper()
	m := decode(h.do("GET", "/api/v1/session", nil))
	user, _ := m["user"].(map[string]any)
	name, _ := user["username"].(string)
	u, err := h.st.UserByName(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

// ageSessions makes every session of a user look verified two hours ago, so step-up is due.
func (h *harness) ageSessions(userID int64) {
	h.t.Helper()
	list, err := h.st.UserSessions(context.Background(), userID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, s := range list {
		_ = h.st.SetSessionVerified(context.Background(), s.IDHash, time.Now().Add(-2*time.Hour))
	}
}

func (h *harness) enableTOTP() (secret string, codes []string) {
	h.t.Helper()
	var setup struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
	if rec := h.do("POST", "/api/v1/account/totp/setup", map[string]any{"password": "not it"}); rec.Code != http.StatusUnauthorized {
		h.t.Fatalf("setting up TOTP without the password: %d", rec.Code)
	}
	h.json(h.do("POST", "/api/v1/account/totp/setup", map[string]any{"password": adminPassword}), &setup)
	if !strings.HasPrefix(setup.URI, "otpauth://totp/") {
		h.t.Fatalf("setup %+v", setup)
	}
	code, _ := auth.TOTPCode(setup.Secret, auth.TOTPStep(time.Now()))
	rec := h.do("POST", "/api/v1/account/totp/enable", map[string]any{"code": code})
	var on struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	h.json(rec, &on)
	if rec.Code != http.StatusOK || len(on.RecoveryCodes) != 10 {
		h.t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	return setup.Secret, on.RecoveryCodes
}

// password signs in afresh with the admin's password; with TOTP on, that gives a ticket for the code.
func (h *harness) password(ticket *string) {
	h.t.Helper()
	h.signOutLocally()
	if t, ok := decode(h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": adminPassword}))["ticket"].(string); ok {
		*ticket = t
	}
}

// TOTP: the code comes after the password; a code works once; recovery codes work once each.
func TestTOTPSignIn(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	secret, codes := h.enableTOTP()
	if m := decode(h.do("GET", "/api/v1/account", nil)); m["totp"] != true || m["recoveryCodesLeft"] != 10.0 {
		t.Fatalf("account %v", m)
	}

	var ticket string
	h.password(&ticket)
	if ticket == "" || h.cookie != "" {
		t.Fatalf("a password alone signed in (ticket %q, cookie %q)", ticket, h.cookie)
	}
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": "000000"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong code: %d", rec.Code)
	}
	next, _ := auth.TOTPCode(secret, auth.TOTPStep(time.Now())+1) // the current step went to enabling
	rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": next})
	if rec.Code != http.StatusOK || h.cookie == "" {
		t.Fatalf("the code: %d %s", rec.Code, rec.Body)
	}
	if action, _, details := lastAudit(t, h); action != "auth.login" || details["with"] != "totp" {
		t.Errorf("audit %s %v", action, details)
	}
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": next}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a used ticket: %d", rec.Code)
	}

	// The same code again, with a new ticket: replayed, refused.
	h.password(&ticket)
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": next}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a replayed code: %d", rec.Code)
	}
	// A recovery code, once.
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": strings.ToUpper(codes[3])}); rec.Code != http.StatusOK {
		t.Fatalf("a recovery code: %d %s", rec.Code, rec.Body)
	}
	h.password(&ticket)
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": codes[3]}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a used recovery code: %d", rec.Code)
	}
	// Wrong codes count against the account (the harness locks after 3; the used recovery code was the first), so
	// fresh tickets from the password do not buy more guesses: even the right code waits out the lock.
	for range 2 {
		h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": "111111"})
	}
	h.password(&ticket)
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": codes[4]}); rec.Code != http.StatusTooManyRequests ||
		decode(rec)["error"] != "locked" {
		t.Errorf("a code while the account is locked: %d %s", rec.Code, rec.Body)
	}
	h.srv.lockout = auth.NewLockout(3, 15*time.Minute) // the lock runs out
	// Five tries end a ticket, whatever the lock says.
	h.password(&ticket)
	for range 2 {
		h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": "111111"})
	}
	h.srv.lockout = auth.NewLockout(3, 15*time.Minute)
	for range 3 {
		h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": "111111"})
	}
	h.srv.lockout = auth.NewLockout(3, 15*time.Minute)
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": codes[4]}); rec.Code != http.StatusUnauthorized ||
		decode(rec)["error"] != "ticket" {
		t.Errorf("a ticket after five wrong codes: %d %s", rec.Code, rec.Body)
	}
	h.password(&ticket)
	if rec := h.do("POST", "/api/v1/auth/login/code", map[string]any{"ticket": ticket, "code": codes[4]}); rec.Code != http.StatusOK {
		t.Fatalf("sign in again: %d", rec.Code)
	}
	if m := decode(h.do("GET", "/api/v1/account", nil)); m["recoveryCodesLeft"] != 8.0 {
		t.Errorf("codes left %v", m["recoveryCodesLeft"])
	}
	// Off with the password: the password alone signs in again.
	if rec := h.do("POST", "/api/v1/account/totp/disable", map[string]any{"password": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("off with a wrong password: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/account/totp/disable", map[string]any{"password": adminPassword}); rec.Code != http.StatusOK {
		t.Fatalf("off: %d %s", rec.Code, rec.Body)
	}
	ticket = ""
	h.password(&ticket)
	if ticket != "" || h.cookie == "" {
		t.Error("TOTP still asked after switching it off")
	}
}

// Step-up: admin-level changes ask again an hour after the session last proved who it is; signing in counts; the
// user may opt out (which itself takes step-up).
func TestStepUp(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	admin := h.me()
	invite := map[string]any{"username": "bob", "role": "viewer"}
	if rec := h.do("POST", "/api/v1/users", invite); rec.Code != http.StatusCreated {
		t.Fatalf("fresh session: %d %s", rec.Code, rec.Body)
	}
	h.ageSessions(admin.ID)
	invite["username"] = "carol"
	if rec := h.do("POST", "/api/v1/users", invite); rec.Code != http.StatusForbidden || decode(rec)["error"] != "step_up" {
		t.Fatalf("an old session: %d %s", rec.Code, rec.Body)
	}
	if m := decode(h.do("GET", "/api/v1/account", nil)); m["stepUpDue"] != true {
		t.Errorf("account %v", m)
	}
	// Not every admin route asks: reading, and changes that are not admin-level.
	if rec := h.do("GET", "/api/v1/users", nil); rec.Code != http.StatusOK {
		t.Errorf("reading: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/step-up", map[string]any{"password": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/auth/step-up", map[string]any{"password": adminPassword}); rec.Code != http.StatusOK {
		t.Fatalf("step-up: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("POST", "/api/v1/users", invite); rec.Code != http.StatusCreated {
		t.Errorf("after step-up: %d %s", rec.Code, rec.Body)
	}
	if action, _, details := lastAudit(t, h); action != "user.invite" {
		t.Errorf("audit %s %v", action, details)
	}

	// With TOTP, a code does it too (once).
	secret, _ := h.enableTOTP()
	h.ageSessions(admin.ID)
	code, _ := auth.TOTPCode(secret, auth.TOTPStep(time.Now())+1)
	if rec := h.do("POST", "/api/v1/auth/step-up", map[string]any{"code": code}); rec.Code != http.StatusOK {
		t.Fatalf("step-up by code: %d %s", rec.Code, rec.Body)
	}
	h.ageSessions(admin.ID)
	if rec := h.do("POST", "/api/v1/auth/step-up", map[string]any{"code": code}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a replayed code: %d", rec.Code)
	}

	// Opting out takes step-up itself; afterwards nothing asks.
	if rec := h.do("PUT", "/api/v1/account/step-up", map[string]any{"optOut": true}); rec.Code != http.StatusForbidden {
		t.Errorf("opting out without step-up: %d", rec.Code)
	}
	h.do("POST", "/api/v1/auth/step-up", map[string]any{"password": adminPassword})
	if rec := h.do("PUT", "/api/v1/account/step-up", map[string]any{"optOut": true}); rec.Code != http.StatusOK || decode(rec)["stepUpOptOut"] != true {
		t.Fatalf("opt out: %d %s", rec.Code, rec.Body)
	}
	h.ageSessions(admin.ID)
	invite["username"] = "dave"
	if rec := h.do("POST", "/api/v1/users", invite); rec.Code != http.StatusCreated {
		t.Errorf("opted out: %d %s", rec.Code, rec.Body)
	}
}

// The admin-level routes, exactly: people, credentials, every config write and restores, exposure, opting out of
// step-up, and backups' passphrase, schedule, deletion and restore.
func TestStepUpRoutes(t *testing.T) {
	h := newHarness(t, nil, fast)
	var got []string
	for _, r := range h.srv.Routes() {
		if r.StepUp {
			got = append(got, r.Method+" "+r.Pattern)
		}
	}
	want := []string{
		"POST /api/v1/users", "POST /api/v1/users/{id}/join-code", "PATCH /api/v1/users/{id}", "DELETE /api/v1/users/{id}",
		"DELETE /api/v1/users/{id}/sessions", "DELETE /api/v1/users/{id}/second-factor",
		"POST /api/v1/credentials", "POST /api/v1/credentials/{name}/revoke",
		"PUT /api/v1/exposure/{id}", "DELETE /api/v1/exposure/{id}", "POST /api/v1/exposure/close-all", "PUT /api/v1/exposure/auto",
		"PUT /api/v1/config", "POST /api/v1/config/snapshots/{id}/restore", "PUT /api/v1/account/step-up",
		"PUT /api/v1/backups/passphrase", "PUT /api/v1/backups/schedule", "DELETE /api/v1/backups/{name}",
		"POST /api/v1/backups/{name}/restore",
		"PATCH /api/v1/config/global", "PATCH /api/v1/config/path-defaults", "PUT /api/v1/config/paths/*",
		"DELETE /api/v1/config/paths/*",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("step-up routes\n got %v\nwant %v", got, want)
	}
}

// virtualKey is a passkey on a virtual authenticator, for the harness's relying party.
type virtualKey struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func (h *harness) registerPasskey(name string) *virtualKey {
	h.t.Helper()
	var begin struct {
		ID      string          `json:"id"`
		Options json.RawMessage `json:"options"`
	}
	if rec := h.do("POST", "/api/v1/account/passkeys/begin", map[string]any{}); rec.Code != http.StatusUnauthorized {
		h.t.Fatalf("adding a passkey without the password: %d", rec.Code)
	}
	h.json(h.do("POST", "/api/v1/account/passkeys/begin", map[string]any{"password": adminPassword}), &begin)
	opts, err := virtualwebauthn.ParseAttestationOptions(string(begin.Options))
	if err != nil {
		h.t.Fatal(err)
	}
	handle := []byte(opts.UserID) // the library hands it over decoded
	k := &virtualKey{
		rp:   virtualwebauthn.RelyingParty{ID: "mtx.example.com", Name: "MediaMTX UI", Origin: origin},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{UserHandle: handle}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
	resp := virtualwebauthn.CreateAttestationResponse(k.rp, k.auth, k.cred, *opts)
	rec := h.do("POST", "/api/v1/account/passkeys/finish", map[string]any{"id": begin.ID, "name": name, "credential": json.RawMessage(resp)})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	return k
}

// assert answers a passkey prompt (path: the begin endpoint) and posts the answer to finish; it returns the status.
func (h *harness) assert(k *virtualKey, rp virtualwebauthn.RelyingParty, begin, finish string) int {
	h.t.Helper()
	var b struct {
		ID      string          `json:"id"`
		Options json.RawMessage `json:"options"`
	}
	h.json(h.do("POST", begin, nil), &b)
	opts, err := virtualwebauthn.ParseAssertionOptions(string(b.Options))
	if err != nil {
		h.t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAssertionResponse(rp, k.auth, k.cred, *opts)
	return h.do("POST", finish, map[string]any{"id": b.ID, "credential": json.RawMessage(resp)}).Code
}

// Passkeys: register, sign in with no username or password, step up; a counter that goes back or another site's
// assertion is refused; removing the last passkey leaves the password.
func TestPasskeys(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	admin := h.me()
	k := h.registerPasskey("Laptop")
	var acct AccountInfo
	h.json(h.do("GET", "/api/v1/account", nil), &acct)
	if len(acct.Passkeys) != 1 || acct.Passkeys[0].Name != "Laptop" || !acct.PasskeysAvailable {
		t.Fatalf("account %+v", acct)
	}

	h.signOutLocally()
	k.cred.Counter = 5
	if code := h.assert(k, k.rp, "/api/v1/auth/passkey/begin", "/api/v1/auth/passkey/finish"); code != http.StatusOK || h.cookie == "" {
		t.Fatalf("passkey sign-in: %d", code)
	}
	if u := h.me(); u.ID != admin.ID {
		t.Errorf("signed in as %s", u.Username)
	}
	if action, _, details := lastAudit(t, h); action != "auth.login" || details["with"] != "passkey" || details["result"] != "ok" {
		t.Errorf("audit %s %v", action, details)
	}

	h.signOutLocally()
	k.cred.Counter = 3 // went backwards: a copied authenticator
	if code := h.assert(k, k.rp, "/api/v1/auth/passkey/begin", "/api/v1/auth/passkey/finish"); code != http.StatusUnauthorized || h.cookie != "" {
		t.Errorf("a regressed counter: %d", code)
	}
	evil := virtualwebauthn.RelyingParty{ID: "evil.example", Name: "Evil", Origin: "https://evil.example"}
	k.cred.Counter = 10
	if code := h.assert(k, evil, "/api/v1/auth/passkey/begin", "/api/v1/auth/passkey/finish"); code != http.StatusUnauthorized {
		t.Errorf("another site's assertion: %d", code)
	}

	// Step-up with the passkey.
	k.cred.Counter = 11
	if code := h.assert(k, k.rp, "/api/v1/auth/passkey/begin", "/api/v1/auth/passkey/finish"); code != http.StatusOK {
		t.Fatalf("sign in again: %d", code)
	}
	h.ageSessions(admin.ID)
	k.cred.Counter = 12
	if code := h.assert(k, k.rp, "/api/v1/auth/step-up/passkey/begin", "/api/v1/auth/step-up/passkey/finish"); code != http.StatusOK {
		t.Fatalf("step-up by passkey: %d", code)
	}
	if m := decode(h.do("GET", "/api/v1/account", nil)); m["stepUpDue"] != false {
		t.Errorf("still due: %v", m)
	}

	// Rename, then remove the only passkey: the password still signs in.
	pid := itoa(acct.Passkeys[0].ID)
	if rec := h.do("PATCH", "/api/v1/account/passkeys/"+pid, map[string]any{"name": "Old laptop"}); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "Old laptop") {
		t.Errorf("rename: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("DELETE", "/api/v1/account/passkeys/"+pid, nil); rec.Code != http.StatusOK {
		t.Fatalf("remove: %d", rec.Code)
	}
	h.signOutLocally()
	k.cred.Counter = 13
	if code := h.assert(k, k.rp, "/api/v1/auth/passkey/begin", "/api/v1/auth/passkey/finish"); code != http.StatusUnauthorized {
		t.Errorf("a removed passkey: %d", code)
	}
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": adminPassword}); rec.Code != http.StatusOK {
		t.Errorf("the password after the last passkey: %d", rec.Code)
	}
}

// Passkeys need a secure origin: on plain HTTP they are not offered.
func TestPasskeysNeedHTTPS(t *testing.T) {
	h := newHarness(t, map[string]string{"MTXUI_PUBLIC_URL": "http://192.168.1.10:9080"}, fast)
	if rec := h.do("POST", "/api/v1/auth/passkey/begin", nil, fromOrigin("http://192.168.1.10:9080")); rec.Code != http.StatusNotFound {
		t.Errorf("passkeys over plain HTTP: %d", rec.Code)
	}
}

// The account: password change (other sessions end), sessions listed and ended.
func TestAccount(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	first := [2]string{h.cookie, h.csrf}
	h.signOutLocally()
	h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": adminPassword})
	var sessions []SessionView
	h.json(h.do("GET", "/api/v1/account/sessions", nil), &sessions)
	if len(sessions) != 2 || !sessions[0].Current || sessions[1].Current || len(sessions[1].Handle) != 16 {
		t.Fatalf("sessions %+v", sessions)
	}
	if rec := h.do("POST", "/api/v1/account/password", map[string]any{"current": "nope", "new": "a much better password 1"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong current password: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/account/password", map[string]any{"current": adminPassword, "new": "short"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a weak password: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/account/password", map[string]any{"current": adminPassword, "new": "a much better password 1"}); rec.Code != http.StatusNoContent {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	h.json(h.do("GET", "/api/v1/account/sessions", nil), &sessions)
	if len(sessions) != 1 {
		t.Errorf("other sessions kept: %+v", sessions)
	}
	mine := [2]string{h.cookie, h.csrf}
	h.cookie, h.csrf = first[0], first[1]
	if rec := h.do("GET", "/api/v1/account", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the other session after a password change: %d", rec.Code)
	}
	h.cookie, h.csrf = mine[0], mine[1]
	if rec := h.do("DELETE", "/api/v1/account/sessions/0000000000000000", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown session: %d", rec.Code)
	}
	if rec := h.do("DELETE", "/api/v1/account/sessions/others", nil); rec.Code != http.StatusNoContent {
		t.Errorf("sign out the others: %d", rec.Code)
	}
}

// An admin disables an account (its sessions end, it cannot sign in), signs a person out and resets their second
// factor; never their own account or the last admin.
func TestPeopleControls(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	admin := h.me()
	hash, _ := h.srv.d.Hasher.Hash(context.Background(), "viewer password 123")
	bob, err := h.st.CreateUser(context.Background(), "bob", hash, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if rec := h.do("PATCH", "/api/v1/users/"+itoa(admin.ID), map[string]any{"disabled": true}); rec.Code != http.StatusConflict {
		t.Errorf("disabling yourself: %d", rec.Code)
	}
	adminSide := [2]string{h.cookie, h.csrf}
	h.signOutLocally()
	h.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": "viewer password 123"})
	bobSide := [2]string{h.cookie, h.csrf}
	h.cookie, h.csrf = adminSide[0], adminSide[1]
	var sessions []SessionView
	h.json(h.do("GET", "/api/v1/users/"+itoa(bob.ID)+"/sessions", nil), &sessions)
	if len(sessions) != 1 {
		t.Errorf("bob's sessions %+v", sessions)
	}
	if rec := h.do("PATCH", "/api/v1/users/"+itoa(bob.ID), map[string]any{"disabled": true}); rec.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	h.cookie, h.csrf = bobSide[0], bobSide[1]
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled user's session: %d", rec.Code)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "bob", "password": "viewer password 123"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled user signs in: %d", rec.Code)
	}
	h.cookie, h.csrf = adminSide[0], adminSide[1]
	if rec := h.do("PATCH", "/api/v1/users/"+itoa(bob.ID), map[string]any{"disabled": false}); rec.Code != http.StatusNoContent {
		t.Fatalf("enable: %d", rec.Code)
	}
	if err := h.st.SetTOTP(context.Background(), bob.ID, "sealed"); err != nil {
		t.Fatal(err)
	}
	if rec := h.do("DELETE", "/api/v1/users/"+itoa(bob.ID)+"/second-factor", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d", rec.Code)
	}
	if u, _ := h.st.UserByID(context.Background(), bob.ID); u.TOTPEnc != "" {
		t.Error("TOTP kept after a reset")
	}
	if action, target, _ := lastAudit(t, h); action != "user.second_factor.reset" || target != "bob" {
		t.Errorf("audit %s %s", action, target)
	}
	if rec := h.do("DELETE", "/api/v1/users/"+itoa(bob.ID)+"/sessions", nil); rec.Code != http.StatusNoContent {
		t.Errorf("sign out everywhere: %d", rec.Code)
	}
	var people []Person
	h.json(h.do("GET", "/api/v1/users", nil), &people)
	for _, p := range people {
		if p.Username == "bob" && (p.Disabled || p.TOTP) {
			t.Errorf("bob %+v", p)
		}
	}
}

// The audit viewer: filters, paging, export (CSV cells that look like formulas are defused).
func TestAuditViewer(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	ctx := context.Background()
	for i, e := range []store.AuditEvent{
		{Actor: "system", Action: "recordings.prune", Target: "recordings"},
		{Actor: "alice", Action: "stream.update", Target: "live/alice"},
		{Actor: "alice", Action: "stream.delete", Target: "=HYPERLINK(\"x\")"},
	} {
		e.At = time.Now().Add(time.Duration(i) * time.Second)
		if err := h.st.InsertAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var list []AuditEntry
	h.json(h.do("GET", "/api/v1/audit?actor=ALICE&action=stream.", nil), &list)
	if len(list) != 2 || list[0].Action != "stream.delete" || list[1].Action != "stream.update" {
		t.Fatalf("filtered %+v", list)
	}
	h.json(h.do("GET", "/api/v1/audit?action=stream.&limit=1", nil), &list)
	if len(list) != 1 {
		t.Fatalf("limited %+v", list)
	}
	h.json(h.do("GET", "/api/v1/audit?action=stream.&before="+itoa(list[0].ID), nil), &list)
	if len(list) != 1 || list[0].Action != "stream.update" {
		t.Errorf("paged %+v", list)
	}
	h.json(h.do("GET", "/api/v1/audit?target=%25", nil), &list) // a literal %, not a wildcard
	if len(list) != 0 {
		t.Errorf("%% matched %+v", list)
	}
	rec := h.do("GET", "/api/v1/audit/export?actor=alice&format=csv", nil)
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if rec.Code != http.StatusOK || err != nil || len(rows) != 3 || !strings.HasPrefix(rows[1][5], "'=") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), ".csv") {
		t.Errorf("csv: %d %v %q", rec.Code, err, rows)
	}
	if rec := h.do("GET", "/api/v1/audit/export?format=json", nil); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "[") {
		t.Errorf("json: %d", rec.Code)
	}
	if rec := h.do("GET", "/api/v1/audit/export?format=xls", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("xls: %d", rec.Code)
	}
}

// A stranger who fails sign-ins for a username locks that username only from their own address: the real user signs
// in from elsewhere, and a signed-in user's step-up and password checks have a lockout of their own.
func TestLockoutIsolation(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	stranger := header("X-Forwarded-For", "198.51.100.66")
	for range 3 {
		h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "guess"}, stranger)
	}
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": adminPassword}, stranger); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the stranger's address after 3 failures: %d", rec.Code)
	}
	h.ageSessions(h.me().ID)
	if rec := h.do("POST", "/api/v1/auth/step-up", map[string]any{"password": adminPassword}); rec.Code != http.StatusOK {
		t.Fatalf("step-up while a stranger locked the username: %d %s", rec.Code, rec.Body)
	}
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": adminPassword}); rec.Code != http.StatusOK {
		t.Fatalf("signing in from the real address: %d %s", rec.Code, rec.Body)
	}
	long := strings.Repeat("a", 8<<10) // under the body limit, far over any username
	if rec := h.do("POST", "/api/v1/auth/login", map[string]any{"username": long, "password": "x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an 8 KiB username: %d", rec.Code)
	}
}

// A join code for someone who had joined resets their password: their other sessions end.
func TestJoinResetEndsSessions(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	var inv struct {
		User     struct{ ID int64 } `json:"user"`
		JoinCode string             `json:"joinCode"`
	}
	h.json(h.do("POST", "/api/v1/users", map[string]any{"username": "dana", "role": "viewer"}), &inv)
	adminCookie, adminCSRF := h.cookie, h.csrf
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "dana's first password"}); rec.Code != http.StatusCreated {
		t.Fatal(rec.Code)
	}
	old := h.cookie
	h.cookie, h.csrf = adminCookie, adminCSRF
	h.json(h.do("POST", "/api/v1/users/"+itoa(inv.User.ID)+"/join-code", nil), &inv)
	h.signOutLocally()
	if rec := h.do("POST", "/api/v1/join", map[string]any{"code": inv.JoinCode, "password": "dana's second password"}); rec.Code != http.StatusCreated {
		t.Fatal(rec.Code)
	}
	h.cookie, h.csrf = old, ""
	if rec := h.do("GET", "/api/v1/session", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the session from before the reset: %d", rec.Code)
	}
}

// A passkey stands in for the password and the code, so its authenticator must verify the user (fingerprint, face,
// PIN): an assertion without user verification is refused, for sign-in and for step-up.
func TestPasskeyNeedsUserVerification(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	k := h.registerPasskey("Laptop")
	lazy := *k
	lazy.auth.Options.UserNotVerified = true
	h.signOutLocally()
	if code := h.assert(&lazy, k.rp, "/api/v1/auth/passkey/begin", "/api/v1/auth/passkey/finish"); code == http.StatusOK {
		t.Fatal("signed in with a passkey that did not verify the user")
	}
	if code := h.assert(k, k.rp, "/api/v1/auth/passkey/begin", "/api/v1/auth/passkey/finish"); code != http.StatusOK {
		t.Fatalf("a verified passkey: %d", code)
	}
	if code := h.assert(&lazy, k.rp, "/api/v1/auth/step-up/passkey/begin", "/api/v1/auth/step-up/passkey/finish"); code == http.StatusOK {
		t.Fatal("stepped up with a passkey that did not verify the user")
	}
}

// The passkey cap holds when a passkey is stored, not only when its prompt starts.
func TestPasskeyCapAtStore(t *testing.T) {
	h := newHarness(t, nil, fast)
	h.completeSetup()
	id := h.me().ID
	for i := range maxPasskeys {
		if _, err := h.st.AddPasskey(context.Background(), store.Passkey{UserID: id, CredentialID: []byte{byte(i)}, Credential: "{}", Name: "k"}, maxPasskeys); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.st.AddPasskey(context.Background(), store.Passkey{UserID: id, CredentialID: []byte{99}, Credential: "{}", Name: "k"}, maxPasskeys); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("an eleventh passkey: %v", err)
	}
}
