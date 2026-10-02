package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"mtxui/internal/store"
)

// ErrNoSession is returned for a missing, unknown, expired or idle session, or one whose user is disabled.
var ErrNoSession = errors.New("no valid session")

// Cookie names. The __Host- prefix makes browsers refuse the cookie unless it is Secure, host-only and Path=/.
const (
	cookieSecure = "__Host-mtxui_session"
	cookiePlain  = "mtxui_session"
)

// Sessions manages server-side sessions. The cookie holds a random token; the database holds only its SHA-256.
type Sessions struct {
	store      *store.Store
	secure     bool
	idle       time.Duration
	maxAge     time.Duration
	touchEvery time.Duration
}

// NewSessions returns a session manager. secure selects Secure, __Host- prefixed cookies (an https PUBLIC_URL).
func NewSessions(s *store.Store, secure bool, idle, maxAge time.Duration) *Sessions {
	return &Sessions{store: s, secure: secure, idle: idle, maxAge: maxAge, touchEvery: time.Minute}
}

// CookieName is the session cookie's name.
func (m *Sessions) CookieName() string {
	if m.secure {
		return cookieSecure
	}
	return cookiePlain
}

// Create starts a session for user and returns the token for the cookie.
func (m *Sessions) Create(ctx context.Context, user store.User, method, ip, userAgent string) (string, store.Session, error) {
	token, err := randomToken()
	if err != nil {
		return "", store.Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", store.Session{}, err
	}
	now := m.store.Now()
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	sess := store.Session{
		IDHash: hashToken(token), UserID: user.ID, CSRFToken: csrf, AuthMethod: method,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(m.maxAge), IP: ip, UserAgent: userAgent,
		VerifiedAt: now, // signing in proves who it is
	}
	return token, sess, m.store.CreateSession(ctx, sess)
}

// Lookup returns the session and user for a token, recording activity at most once a minute.
func (m *Sessions) Lookup(ctx context.Context, token string) (store.Session, store.User, error) {
	if token == "" || len(token) > 128 {
		return store.Session{}, store.User{}, ErrNoSession
	}
	sess, err := m.store.SessionByHash(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, store.User{}, ErrNoSession
	}
	if err != nil {
		return store.Session{}, store.User{}, err
	}
	now := m.store.Now()
	if !now.Before(sess.ExpiresAt) || !now.Before(sess.LastSeenAt.Add(m.idle)) {
		_ = m.store.DeleteSession(ctx, sess.IDHash)
		return store.Session{}, store.User{}, ErrNoSession
	}
	user, err := m.store.UserByID(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && user.Disabled) {
		return store.Session{}, store.User{}, ErrNoSession
	}
	if err != nil {
		return store.Session{}, store.User{}, err
	}
	if now.Sub(sess.LastSeenAt) >= m.touchEvery {
		if err := m.store.TouchSession(ctx, sess.IDHash, now); err == nil {
			sess.LastSeenAt = now
		}
	}
	return sess, user, nil
}

// Destroy ends the session for a token.
func (m *Sessions) Destroy(ctx context.Context, token string) error {
	return m.store.DeleteSession(ctx, hashToken(token))
}

// Sweep deletes expired and idle sessions.
func (m *Sessions) Sweep(ctx context.Context) (int64, error) {
	now := m.store.Now()
	return m.store.DeleteExpiredSessions(ctx, now, now.Add(-m.idle))
}

// SetCookie sets the session cookie, expiring with the session.
func (m *Sessions) SetCookie(w http.ResponseWriter, token string, expires time.Time) {
	//nolint:gosec // Secure follows PUBLIC_URL: plain-HTTP LAN setups cannot use it; HttpOnly and SameSite are always set
	http.SetCookie(w, &http.Cookie{
		Name: m.CookieName(), Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie removes the session cookie.
func (m *Sessions) ClearCookie(w http.ResponseWriter) {
	//nolint:gosec // as in SetCookie
	http.SetCookie(w, &http.Cookie{
		Name: m.CookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
	})
}

// TokenFrom returns the session token from a request's cookie.
func (m *Sessions) TokenFrom(r *http.Request) string {
	c, err := r.Cookie(m.CookieName())
	if err != nil {
		return ""
	}
	return c.Value
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
