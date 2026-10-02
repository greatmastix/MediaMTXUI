package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is a UI account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string // admin, operator or viewer
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	TOTPEnc      string // the sealed TOTP secret, or empty when TOTP is off
	TOTPLastStep int64  // the last TOTP time step accepted (a code works once)
	StepUpOptOut bool   // never asked again before admin-level changes
	WebAuthnID   []byte // the passkeys' user handle, or nil before the first passkey
}

// ErrSetupDone is returned by CompleteSetup when setup has already happened.
var ErrSetupDone = errors.New("setup has already been completed")

const metaSetupCompleted = "setup_completed_at"

// SetupCompleted reports whether first-run setup has happened.
func (s *Store) SetupCompleted(ctx context.Context) (bool, error) {
	_, ok, err := s.Meta(ctx, metaSetupCompleted)
	return ok, err
}

// CompleteSetup creates the first admin and marks setup as completed, atomically: of two concurrent attempts,
// exactly one succeeds and the other gets ErrSetupDone.
func (s *Store) CompleteSetup(ctx context.Context, username, passwordHash string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var done int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key = ?`, metaSetupCompleted).Scan(&done); err != nil {
		return User{}, err
	}
	var users int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return User{}, err
	}
	if done > 0 || users > 0 {
		return User{}, ErrSetupDone
	}
	now := s.now()
	u := User{Username: username, PasswordHash: passwordHash, Role: "admin", CreatedAt: now, UpdatedAt: now}
	res, err := tx.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, 'admin', ?, ?)`, username, passwordHash, ms(now), ms(now))
	if err != nil {
		return User{}, err
	}
	if u.ID, err = res.LastInsertId(); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, metaSetupCompleted, now.UTC().Format(time.RFC3339)); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

// UserByName looks a user up case-insensitively.
func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE username = ?`, username))
}

// UserByID looks a user up by id.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE id = ?`, id))
}

// UpdatePasswordHash stores a new hash, e.g. after rehashing with stronger parameters.
func (s *Store) UpdatePasswordHash(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, ms(s.now()), id)
	return err
}

// RehashPassword replaces a password hash with a fresh one of the same password, only if the stored hash is still old
// (the one just verified): a password changed meanwhile is not overwritten.
func (s *Store) RehashPassword(ctx context.Context, id int64, old, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ? AND password_hash = ?`,
		hash, ms(s.now()), id, old)
	return err
}

// SetUserRole changes a user's role.
func (s *Store) SetUserRole(ctx context.Context, id int64, role string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, ms(s.now()), id)
	return affected(res, err)
}

const userSelect = `SELECT id, username, password_hash, role, disabled, created_at, updated_at, totp_enc,
	totp_last_step, step_up_opt_out, webauthn_id FROM users`

func (s *Store) scanUser(row scanner) (User, error) {
	var u User
	var created, updated int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Disabled, &created, &updated, &u.TOTPEnc,
		&u.TOTPLastStep, &u.StepUpOptOut, &u.WebAuthnID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt, u.UpdatedAt = fromMS(created), fromMS(updated)
	return u, nil
}

// CreateUser adds a user after setup, or returns ErrExists for a taken name.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string) (User, error) {
	if _, err := s.UserByName(ctx, username); err == nil {
		return User{}, ErrExists
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, username, passwordHash, role, ms(now), ms(now))
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, PasswordHash: passwordHash, Role: role, CreatedAt: now, UpdatedAt: now}, nil
}

// ErrLastAdmin is returned when a change would leave no admin who can sign in.
var ErrLastAdmin = errors.New("no admin who can sign in would be left")

// UserChange is a change to a user: a new role, disabled or enabled, or deletion.
type UserChange struct {
	Role     *string
	Disabled *bool
	Delete   bool
}

// ChangeUser applies a change in one transaction and refuses it (ErrLastAdmin) when no admin who can sign in would be
// left. The count is taken inside the transaction, after the change: two admins acting at once cannot both pass a
// check made before either wrote.
func (s *Store) ChangeUser(ctx context.Context, id int64, c UserChange) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	now := ms(s.now())
	if c.Delete {
		if err := affected(tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)); err != nil {
			return err
		}
	}
	if c.Role != nil && !c.Delete {
		if err := affected(tx.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, *c.Role, now, id)); err != nil {
			return err
		}
	}
	if c.Disabled != nil && !c.Delete {
		if err := affected(tx.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, *c.Disabled, now, id)); err != nil {
			return err
		}
	}
	var admins int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND password_hash != '' AND disabled = 0`).Scan(&admins); err != nil {
		return err
	}
	if admins == 0 {
		return ErrLastAdmin
	}
	return tx.Commit()
}
