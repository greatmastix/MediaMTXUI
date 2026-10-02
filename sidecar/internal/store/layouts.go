package store

import (
	"context"
	"errors"
	"time"
)

// Layout is a saved multi-view layout.
type Layout struct {
	Name      string
	Data      string // JSON
	UpdatedAt time.Time
}

// maxLayouts caps how many layouts one user keeps.
const maxLayouts = 50

// ErrTooMany is returned when a user already has the most layouts allowed.
var ErrTooMany = errors.New("too many layouts")

// Layouts returns a user's layouts by name.
func (s *Store) Layouts(ctx context.Context, userID int64) ([]Layout, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, data, updated_at FROM user_layouts WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Layout{}
	for rows.Next() {
		var l Layout
		var at int64
		if err := rows.Scan(&l.Name, &l.Data, &at); err != nil {
			return nil, err
		}
		l.UpdatedAt = fromMS(at)
		out = append(out, l)
	}
	return out, rows.Err()
}

// SaveLayout creates or replaces a user's layout.
func (s *Store) SaveLayout(ctx context.Context, userID int64, name, data string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists, count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_layouts WHERE user_id = ? AND name = ?`, userID, name).Scan(&exists); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_layouts WHERE user_id = ?`, userID).Scan(&count); err != nil {
		return err
	}
	if exists == 0 && count >= maxLayouts {
		return ErrTooMany
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_layouts (user_id, name, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, name) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		userID, name, data, ms(s.now())); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteLayout removes a user's layout, or returns ErrNotFound.
func (s *Store) DeleteLayout(ctx context.Context, userID int64, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_layouts WHERE user_id = ? AND name = ?`, userID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
