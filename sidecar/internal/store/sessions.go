package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a signed-in browser session. IDHash is the SHA-256 of the token in the cookie.
type Session struct {
	IDHash     string
	UserID     int64
	CSRFToken  string
	AuthMethod string // how the user signed in: "password" (later also "passkey", "totp")
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time // absolute expiry; the idle timeout is enforced against LastSeenAt
	IP         string
	UserAgent  string
	VerifiedAt time.Time // when the session last proved who it is (sign-in, step-up); zero for sessions from before step-up existed
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(id_hash, user_id, csrf_token, auth_method, created_at, last_seen_at, expires_at, ip, user_agent, verified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.IDHash, sess.UserID, sess.CSRFToken, sess.AuthMethod, ms(sess.CreatedAt), ms(sess.LastSeenAt),
		ms(sess.ExpiresAt), sess.IP, sess.UserAgent, ms(sess.VerifiedAt))
	return err
}

const sessionSelect = `SELECT id_hash, user_id, csrf_token, auth_method, created_at, last_seen_at, expires_at, ip,
	user_agent, verified_at FROM sessions`

func scanSession(row scanner) (Session, error) {
	var sess Session
	var created, seen, expires, verified int64
	err := row.Scan(&sess.IDHash, &sess.UserID, &sess.CSRFToken, &sess.AuthMethod, &created, &seen, &expires, &sess.IP,
		&sess.UserAgent, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt = fromMS(created), fromMS(seen), fromMS(expires)
	if verified > 0 {
		sess.VerifiedAt = fromMS(verified)
	}
	return sess, nil
}

// SessionByHash looks a session up by the hash of its token.
func (s *Store) SessionByHash(ctx context.Context, idHash string) (Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE id_hash = ?`, idHash))
}

// TouchSession records activity.
func (s *Store) TouchSession(ctx context.Context, idHash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, ms(at), idHash)
	return err
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

// DeleteExpiredSessions removes sessions past their absolute expiry or idle since before idleCutoff.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now, idleCutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?`, ms(now), ms(idleCutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
