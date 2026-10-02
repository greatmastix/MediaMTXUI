package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Forward is a stream re-streamed to another platform. DestEnc is the sealed destination (it carries the platform's
// stream key); Label is where it goes without the key.
type Forward struct {
	ID        int64
	StreamID  int64
	Provider  string
	Label     string
	DestEnc   string
	Enabled   bool
	CreatedAt time.Time
	CreatedBy string
}

const forwardSelect = `SELECT id, stream_id, provider, label, dest_enc, enabled, created_at, created_by FROM stream_forwards`

func scanForward(scan func(...any) error) (Forward, error) {
	var f Forward
	var created int64
	err := scan(&f.ID, &f.StreamID, &f.Provider, &f.Label, &f.DestEnc, &f.Enabled, &created, &f.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Forward{}, ErrNotFound
	}
	f.CreatedAt = fromMS(created)
	return f, err
}

// CreateForward stores a forward.
func (s *Store) CreateForward(ctx context.Context, f Forward) (Forward, error) {
	f.CreatedAt = s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO stream_forwards
		(stream_id, provider, label, dest_enc, enabled, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.StreamID, f.Provider, f.Label, f.DestEnc, f.Enabled, ms(f.CreatedAt), f.CreatedBy)
	if err != nil {
		return Forward{}, err
	}
	f.ID, err = res.LastInsertId()
	return f, err
}

// Forwards lists a stream's forwards in the order they were added.
func (s *Store) Forwards(ctx context.Context, streamID int64) ([]Forward, error) {
	rows, err := s.db.QueryContext(ctx, forwardSelect+` WHERE stream_id = ? ORDER BY id`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Forward
	for rows.Next() {
		f, err := scanForward(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ForwardByID looks up one of a stream's forwards.
func (s *Store) ForwardByID(ctx context.Context, streamID, id int64) (Forward, error) {
	return scanForward(s.db.QueryRowContext(ctx, forwardSelect+` WHERE stream_id = ? AND id = ?`, streamID, id).Scan)
}

// SetForwardEnabled switches a forward on or off.
func (s *Store) SetForwardEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE stream_forwards SET enabled = ? WHERE id = ?`, enabled, id)
	return affected(res, err)
}

// DeleteForward removes a forward.
func (s *Store) DeleteForward(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM stream_forwards WHERE id = ?`, id)
	return affected(res, err)
}
