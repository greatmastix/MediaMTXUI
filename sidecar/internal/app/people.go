package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mtxui/internal/audit"
	"mtxui/internal/auth"
	"mtxui/internal/auth/clientip"
	"mtxui/internal/store"
)

// People (admins): the UI's users, and invitations. An invited account has no password until its owner
// joins with the one-time code the admin passes on; the code is typed into /join, never put in a link, and stored
// only as a hash.

// joinCodeTTL is how long a join code works.
const joinCodeTTL = 72 * time.Hour

var joinB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newJoinCode returns a code as shown (XXXX-XXXX-XXXX-XXXX, 80 bits) and the hash stored for it.
func newJoinCode() (string, string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw := joinB32.EncodeToString(b)
	return raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16], joinCodeHash(raw), nil
}

// joinCodeHash hashes a code as typed: case, dashes and spaces do not matter.
func joinCodeHash(code string) string {
	norm := strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			return -1
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		}
		return r
	}, code)
	sum := sha256.Sum256([]byte("mtxui-join-v1\x00" + norm))
	return hex.EncodeToString(sum[:])
}

// Person is a user as the People page shows it.
type Person struct {
	ID          int64          `json:"id"`
	Username    string         `json:"username"`
	Role        string         `json:"role"`
	Disabled    bool           `json:"disabled"`
	Pending     bool           `json:"pending"` // invited, not joined yet
	JoinExpires *time.Time     `json:"joinExpires"`
	Streams     []StreamOwnerR `json:"streams"`
	CreatedAt   time.Time      `json:"createdAt"`
	TOTP        bool           `json:"totp"`     // an authenticator app is set up
	Passkeys    int            `json:"passkeys"` // passkeys registered
}

// StreamOwnerR is a stream a person owns.
type StreamOwnerR struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (s *Server) usersList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := s.d.Store.Users(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot read the users.")
		return
	}
	streams, _ := s.d.Store.Streams(ctx)
	out := make([]Person, 0, len(users))
	for _, u := range users {
		p := Person{
			ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled, Pending: u.PasswordHash == "",
			Streams: []StreamOwnerR{}, CreatedAt: u.CreatedAt, TOTP: u.TOTPEnc != "",
		}
		if keys, err := s.d.Store.Passkeys(ctx, u.ID); err == nil {
			p.Passkeys = len(keys)
		}
		if p.Pending {
			p.JoinExpires, _ = s.d.Store.JoinCodeExpiry(ctx, u.ID)
		}
		for _, st := range streams {
			if st.OwnerID != nil && *st.OwnerID == u.ID {
				p.Streams = append(p.Streams, StreamOwnerR{st.ID, st.Name})
			}
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// Invitation is the answer to an invite: the code is shown this once.
type Invitation struct {
	User     Person    `json:"user"`
	JoinCode string    `json:"joinCode"`
	Expires  time.Time `json:"expires"`
}

func (s *Server) userInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := auth.ValidateUsername(req.Username); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if _, err := auth.ParseRole(req.Role); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Role must be streamer, viewer, operator or admin.")
		return
	}
	u, err := s.d.Store.CreateUser(ctx, req.Username, "", req.Role)
	if errors.Is(err, store.ErrExists) {
		writeError(w, http.StatusConflict, "exists", "This username is taken.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot create the user.")
		return
	}
	inv, err := s.issueJoinCode(r, u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The user was created, but no join code could be made. Make a new one.")
		return
	}
	audit.Set(ctx, "user.invite", u.Username, map[string]any{"role": u.Role, "expires": inv.Expires})
	writeJSON(w, http.StatusCreated, inv)
}

// IssueJoinCode makes a new join code for u (an unused older one stops working) and returns it as people type it,
// with its expiry. For someone who has joined, it is a password reset. by names who made it, for the record.
func IssueJoinCode(ctx context.Context, st *store.Store, u store.User, by string) (string, time.Time, error) {
	code, hash, err := newJoinCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := st.Now().Add(joinCodeTTL).Truncate(time.Second)
	if err := st.CreateJoinCode(ctx, u.ID, hash, expires, by); err != nil {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

func (s *Server) issueJoinCode(r *http.Request, u store.User) (Invitation, error) {
	code, expires, err := IssueJoinCode(r.Context(), s.d.Store, u, s.author(r))
	if err != nil {
		return Invitation{}, err
	}
	return Invitation{
		User:     Person{ID: u.ID, Username: u.Username, Role: u.Role, Pending: u.PasswordHash == "", JoinExpires: &expires, Streams: []StreamOwnerR{}, CreatedAt: u.CreatedAt},
		JoinCode: code, Expires: expires,
	}, nil
}

func (s *Server) userFromURL(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err == nil {
		if u, err := s.d.Store.UserByID(r.Context(), id); err == nil {
			return u, true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "No such user.")
	return store.User{}, false
}

// userJoinCode makes a new join code (the old one stops working). For someone who has joined, it is a password reset:
// their password keeps working until they use the code.
func (s *Server) userJoinCode(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userFromURL(w, r)
	if !ok {
		return
	}
	inv, err := s.issueJoinCode(r, u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "No join code could be made.")
		return
	}
	audit.Set(r.Context(), "user.join-code", u.Username, map[string]any{"expires": inv.Expires})
	writeJSON(w, http.StatusOK, inv)
}

// userPatch changes a user's role or disables (enables) the account. Admins cannot change their own role or disable
// themselves (no locking yourself out), and the last admin stays one. A change applies to the user's next request;
// their open event streams end so pages reload, and disabling ends their sessions.
func (s *Server) userPatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := s.userFromURL(w, r)
	if !ok {
		return
	}
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, _ := current(ctx)
	var change store.UserChange
	if req.Role != nil && *req.Role != u.Role {
		if _, err := auth.ParseRole(*req.Role); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "Role must be streamer, viewer, operator or admin.")
			return
		}
		if cur.user.ID == u.ID {
			writeError(w, http.StatusConflict, "self", "You cannot change your own role.")
			return
		}
		change.Role = req.Role
	}
	if req.Disabled != nil && *req.Disabled != u.Disabled {
		if cur.user.ID == u.ID {
			writeError(w, http.StatusConflict, "self", "You cannot disable yourself.")
			return
		}
		change.Disabled = req.Disabled
	}
	if change.Role == nil && change.Disabled == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch err := s.d.Store.ChangeUser(ctx, u.ID, change); {
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", "This is the last admin.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "Cannot change the account.")
		return
	}
	if change.Role != nil {
		audit.Set(ctx, "user.role", u.Username, map[string]any{"from": u.Role, "to": *change.Role})
	}
	if change.Disabled != nil {
		if *change.Disabled {
			_, _ = s.d.Store.DeleteUserSessionsExcept(ctx, u.ID, "")
		}
		audit.Set(ctx, "user.disable", u.Username, map[string]any{"disabled": *change.Disabled})
	}
	s.sessionNudge.fire()
	w.WriteHeader(http.StatusNoContent)
}

// userSessions lists a person's sessions (for an admin).
func (s *Server) userSessions(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userFromURL(w, r)
	if !ok {
		return
	}
	views, err := s.sessionViews(r, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot list the sessions.")
		return
	}
	writeJSON(w, http.StatusOK, views)
}

// userSessionsEnd signs a person out everywhere.
func (s *Server) userSessionsEnd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := s.userFromURL(w, r)
	if !ok {
		return
	}
	cur, _ := current(ctx)
	keep := ""
	if cur.user.ID == u.ID {
		keep = cur.session.IDHash // yourself: everywhere else
	}
	n, err := s.d.Store.DeleteUserSessionsExcept(ctx, u.ID, keep)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot sign them out.")
		return
	}
	s.sessionNudge.fire()
	audit.Set(ctx, "user.sessions.end", u.Username, map[string]any{"ended": n})
	w.WriteHeader(http.StatusNoContent)
}

// userSecondFactorReset takes away a person's authenticator app, recovery codes and passkeys (a lost phone or key);
// they sign in with their password and set them up again.
func (s *Server) userSecondFactorReset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := s.userFromURL(w, r)
	if !ok {
		return
	}
	if err := s.d.Store.RemoveSecondFactors(ctx, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Cannot reset them.")
		return
	}
	audit.Set(ctx, "user.second_factor.reset", u.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) userDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userFromURL(w, r)
	if !ok {
		return
	}
	if cur, _ := current(r.Context()); cur.user.ID == u.ID {
		writeError(w, http.StatusConflict, "self", "You cannot delete yourself.")
		return
	}
	// Sessions, layouts and codes go with the user.
	switch err := s.d.Store.ChangeUser(r.Context(), u.ID, store.UserChange{Delete: true}); {
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", "This is the last admin.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", "Cannot delete the user.")
		return
	}
	_ = s.reloadOwners(r.Context())
	s.sessionNudge.fire()
	audit.Set(r.Context(), "user.delete", u.Username, map[string]any{"role": u.Role})
	w.WriteHeader(http.StatusNoContent)
}

// join lets an invited person set a password with their code, and signs them in. Rate-limited per address like
// setup; a wrong code says nothing about which codes or users exist.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if ok, wait := s.setupRate.Allow(clientip.RateKey(clientip.From(ctx).IP)); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("Too many attempts. Try again in %d seconds.", retryAfter(w, wait)))
		return
	}
	var body struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	hash := joinCodeHash(body.Code)
	u, err := s.d.Store.JoinCodeUser(ctx, hash)
	if err != nil {
		audit.Set(ctx, "user.join", "", map[string]any{"result": "unknown code"})
		writeError(w, http.StatusUnauthorized, "invalid_code", "This join code is wrong, used or expired. Ask for a new one.")
		return
	}
	if err := auth.ValidatePassword(body.Password, u.Username); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	pw, err := s.d.Hasher.Hash(ctx, body.Password)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "busy", "The server is busy. Try again.")
		return
	}
	u, err = s.d.Store.RedeemJoinCode(ctx, hash, pw)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_code", "This join code is wrong, used or expired. Ask for a new one.")
		return
	}
	audit.Actor(ctx, u.Username, &u.ID)
	audit.Set(ctx, "user.join", u.Username, map[string]any{"result": "ok"})
	// A join code for someone who had joined is a password reset: whoever held their old sessions is signed out.
	if _, err := s.d.Store.DeleteUserSessionsExcept(ctx, u.ID, ""); err == nil {
		s.sessionNudge.fire()
	}
	sess, err := s.startSession(w, r, u, "password")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "You have joined, but signing in failed. Sign in with your new password.")
		return
	}
	writeJSON(w, http.StatusCreated, sessionInfo(sess, u))
}
