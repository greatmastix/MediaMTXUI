package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

// Second factors and step-up.

// SetUserDisabled disables or enables an account.
func (s *Store) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, disabled, ms(s.now()), id)
	return affected(res, err)
}

// SetStepUpOptOut records whether the user is asked again before admin-level changes.
func (s *Store) SetStepUpOptOut(ctx context.Context, id int64, optOut bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET step_up_opt_out = ?, updated_at = ? WHERE id = ?`, optOut, ms(s.now()), id)
	return affected(res, err)
}

// SetTOTP stores a user's sealed TOTP secret (empty: TOTP off) and forgets the last step used.
func (s *Store) SetTOTP(ctx context.Context, id int64, enc string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_enc = ?, totp_last_step = 0, updated_at = ? WHERE id = ?`,
		enc, ms(s.now()), id)
	return affected(res, err)
}

// UseTOTPStep accepts a TOTP time step for a user once: false when it, or a later one, was used already.
func (s *Store) UseTOTPStep(ctx context.Context, id, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReplaceRecoveryCodes sets a user's recovery codes (their hashes), dropping the old ones.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, id int64, hashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, id); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, id, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UseRecoveryCode spends one of a user's recovery codes: false when it is not theirs or was used.
func (s *Store) UseRecoveryCode(ctx context.Context, id int64, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		ms(s.now()), id, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecoveryCodesLeft counts a user's unused recovery codes.
func (s *Store) RecoveryCodesLeft(ctx context.Context, id int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, id).Scan(&n)
	return n, err
}

// RemoveSecondFactors takes away a user's TOTP, recovery codes and passkeys (an admin's reset for a lost device).
func (s *Store) RemoveSecondFactors(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	for _, q := range []string{
		`UPDATE users SET totp_enc = '', totp_last_step = 0 WHERE id = ?`,
		`DELETE FROM recovery_codes WHERE user_id = ?`,
		`DELETE FROM passkeys WHERE user_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// WebAuthnID returns the user's passkey handle, making one on first use.
func (s *Store) WebAuthnID(ctx context.Context, id int64) ([]byte, error) {
	var h []byte
	if err := s.db.QueryRowContext(ctx, `SELECT webauthn_id FROM users WHERE id = ?`, id).Scan(&h); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(h) > 0 {
		return h, nil
	}
	h = make([]byte, 32)
	if _, err := rand.Read(h); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET webauthn_id = ? WHERE id = ? AND webauthn_id IS NULL`, h, id); err != nil {
		return nil, err
	}
	return s.WebAuthnID(ctx, id)
}

// UserByWebAuthnID finds the user a passkey's handle belongs to.
func (s *Store) UserByWebAuthnID(ctx context.Context, h []byte) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE webauthn_id = ?`, h))
}

// Passkey is a registered WebAuthn credential.
type Passkey struct {
	ID           int64
	UserID       int64
	CredentialID []byte
	Credential   string // the library's credential as JSON
	Name         string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

//nolint:gosec // a column list, not a credential
const passkeySelect = `SELECT id, user_id, credential_id, credential, name, created_at, last_used_at FROM passkeys`

func scanPasskey(row scanner) (Passkey, error) {
	var p Passkey
	var created int64
	var used sql.NullInt64
	err := row.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.Credential, &p.Name, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrNotFound
	}
	p.CreatedAt, p.LastUsedAt = fromMS(created), fromNullMS(used)
	return p, err
}

// ErrLimit is returned when a user already has as many of something as allowed.
var ErrLimit = errors.New("limit reached")

// AddPasskey stores a passkey unless the user already has max: the count is part of the insert, so parallel
// registrations cannot get past it.
func (s *Store) AddPasskey(ctx context.Context, p Passkey, maxPerUser int) (Passkey, error) {
	p.CreatedAt = s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO passkeys (user_id, credential_id, credential, name, created_at)
		SELECT ?, ?, ?, ?, ? WHERE (SELECT count(*) FROM passkeys WHERE user_id = ?) < ?`,
		p.UserID, p.CredentialID, p.Credential, p.Name, ms(p.CreatedAt), p.UserID, maxPerUser)
	if isUniqueViolation(err) {
		return Passkey{}, ErrExists
	}
	if err != nil {
		return Passkey{}, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Passkey{}, ErrLimit
	}
	p.ID, err = res.LastInsertId()
	return p, err
}

// Passkeys lists a user's passkeys, oldest first.
func (s *Store) Passkeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, passkeySelect+` WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PasskeyUsed stores a passkey's credential after a sign-in (its sign counter moves on) and when.
func (s *Store) PasskeyUsed(ctx context.Context, id int64, credential string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE passkeys SET credential = ?, last_used_at = ? WHERE id = ?`, credential, ms(s.now()), id)
	return affected(res, err)
}

// RenamePasskey renames one of a user's passkeys.
func (s *Store) RenamePasskey(ctx context.Context, userID, id int64, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	return affected(res, err)
}

// DeletePasskey removes one of a user's passkeys.
func (s *Store) DeletePasskey(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
	return affected(res, err)
}

// SetSessionVerified records that a session just proved who it is (sign-in or step-up).
func (s *Store) SetSessionVerified(ctx context.Context, idHash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET verified_at = ? WHERE id_hash = ?`, ms(at), idHash)
	return err
}

// UserSessions lists a user's sessions, newest activity first.
func (s *Store) UserSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionSelect+` WHERE user_id = ? ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteUserSessionsExcept ends a user's sessions but one (keep may be empty).
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID int64, keep string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`, userID, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
