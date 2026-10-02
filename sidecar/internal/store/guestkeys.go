package store

import (
	"context"
	"time"
)

// GuestKey is a time-limited key for one stream: its kind (publish or read), who it is for, and its credential.
type GuestKey struct {
	StreamID   int64
	Kind       string
	Label      string
	Credential Credential
}

// Guest key kinds.
const (
	GuestPublish = "publish"
	GuestRead    = "read"
)

// AddGuestKey ties a credential to a stream as a guest key.
func (s *Store) AddGuestKey(ctx context.Context, streamID, credentialID int64, kind, label string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO stream_guest_keys (credential_id, stream_id, kind, label) VALUES (?, ?, ?, ?)`,
		credentialID, streamID, kind, label)
	return err
}

// guestSelect joins a guest key with its credential, columns in scanCredential's order after the guest's own.
const guestSelect = `SELECT g.stream_id, g.kind, g.label, c.id, c.name, c.kind, c.secret_hash, c.actions, c.paths,
	c.source_cidrs, c.expires_at, c.revoked_at, c.created_at, c.created_by, c.last_used_at
	FROM stream_guest_keys g JOIN stream_credentials c ON c.id = g.credential_id`

func (s *Store) guestKeys(ctx context.Context, limit int, where string, args ...any) ([]GuestKey, error) {
	rows, err := s.db.QueryContext(ctx, guestSelect+` WHERE `+where+` ORDER BY c.created_at DESC, c.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GuestKey
	for rows.Next() {
		var g GuestKey
		c, err := scanCredential(prefixed{rows, []any{&g.StreamID, &g.Kind, &g.Label}})
		if err != nil {
			return nil, err
		}
		g.Credential = c
		out = append(out, g)
	}
	return out, rows.Err()
}

// prefixed scans leading columns into head, the rest as the wrapped scanner's caller asks.
type prefixed struct {
	row  scanner
	head []any
}

func (p prefixed) Scan(dest ...any) error { return p.row.Scan(append(p.head, dest...)...) }

// GuestKeys lists a stream's guest keys, newest first, expired and revoked ones included (at most 50).
func (s *Store) GuestKeys(ctx context.Context, streamID int64) ([]GuestKey, error) {
	return s.guestKeys(ctx, 50, `g.stream_id = ?`, streamID)
}

// GuestKey looks up one of a stream's guest keys by its credential id.
func (s *Store) GuestKey(ctx context.Context, streamID, credentialID int64) (GuestKey, error) {
	list, err := s.guestKeys(ctx, 1, `g.stream_id = ? AND g.credential_id = ?`, streamID, credentialID)
	if err != nil {
		return GuestKey{}, err
	}
	if len(list) == 0 {
		return GuestKey{}, ErrNotFound
	}
	return list[0], nil
}

// ActiveGuestKeys lists the guest keys of every stream that are neither revoked nor expired at now.
func (s *Store) ActiveGuestKeys(ctx context.Context, now time.Time) ([]GuestKey, error) {
	return s.guestKeys(ctx, 10000, `c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at > ?)`, ms(now))
}

// ExpiredGuestKeys lists the guest keys that expired between since and now and were not revoked.
func (s *Store) ExpiredGuestKeys(ctx context.Context, since, now time.Time) ([]GuestKey, error) {
	return s.guestKeys(ctx, 10000, `c.revoked_at IS NULL AND c.expires_at <= ? AND c.expires_at > ?`, ms(now), ms(since))
}
