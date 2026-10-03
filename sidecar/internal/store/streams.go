package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Stream is a MediaMTX path presented as a stream page, with its owner and keys.
type Stream struct {
	ID             int64
	Name           string // the MediaMTX path
	Title          string
	OwnerID        *int64
	Target         string // a target preset id, or empty
	PublishKeyID   *int64
	PublishKeyEnc  string // encrypted secret of the publish key, or empty
	PlaybackKeyID  *int64
	PlaybackKeyEnc string
	Public         bool   // anyone may watch without a key
	Audio          string // the audio codec its encoder sends: aac or opus (a holding clip must match)
	Holding        string // what plays while nobody streams: "", "builtin" or "file"
	ClipAAC        string // the own holding clip's file for encoders that send AAC (RTMP, SRT), or empty
	ClipOpus       string // the same clip for encoders that send Opus (WHIP), or empty
	Format         string // the stream's video format, which holding clips are in: 720p50 ... 1080p60
	CreatedAt      time.Time
	CreatedBy      string
}

// Key kinds of a stream.
const (
	KeyPublish  = "publish"
	KeyPlayback = "playback"
)

const streamSelect = `SELECT id, name, title, owner_id, target, publish_key_id, COALESCE(publish_key_enc, ''),
	playback_key_id, COALESCE(playback_key_enc, ''), public, audio, holding, clip_aac, clip_opus, format, created_at, created_by FROM streams`

func scanStream(scan func(...any) error) (Stream, error) {
	var st Stream
	var created int64
	err := scan(&st.ID, &st.Name, &st.Title, &st.OwnerID, &st.Target, &st.PublishKeyID, &st.PublishKeyEnc,
		&st.PlaybackKeyID, &st.PlaybackKeyEnc, &st.Public, &st.Audio, &st.Holding, &st.ClipAAC, &st.ClipOpus, &st.Format, &created, &st.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Stream{}, ErrNotFound
	}
	st.CreatedAt = fromMS(created)
	return st, err
}

// CreateStream stores a stream without keys, or returns ErrExists for a taken name.
func (s *Store) CreateStream(ctx context.Context, st Stream) (Stream, error) {
	st.CreatedAt = s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO streams (name, title, owner_id, target, public, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, st.Name, st.Title, st.OwnerID, st.Target, st.Public, ms(st.CreatedAt), st.CreatedBy)
	if isUniqueViolation(err) {
		return Stream{}, ErrExists
	}
	if err != nil {
		return Stream{}, err
	}
	st.ID, err = res.LastInsertId()
	return st, err
}

// StreamByID looks a stream up.
func (s *Store) StreamByID(ctx context.Context, id int64) (Stream, error) {
	return scanStream(s.db.QueryRowContext(ctx, streamSelect+` WHERE id = ?`, id).Scan)
}

// Streams lists every stream by name.
func (s *Store) Streams(ctx context.Context) ([]Stream, error) {
	rows, err := s.db.QueryContext(ctx, streamSelect+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stream
	for rows.Next() {
		st, err := scanStream(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// UpdateStream saves title, owner, target, whether it is public, its audio and its holding screen. It writes every
// column from st: a request that read st earlier undoes whatever changed since, so handlers use PatchStream.
func (s *Store) UpdateStream(ctx context.Context, st Stream) error {
	if st.Audio == "" {
		st.Audio = "aac"
	}
	if st.Format == "" {
		st.Format = "1080p50"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE streams SET title = ?, owner_id = ?, target = ?, public = ?, audio = ?,
		holding = ?, clip_aac = ?, clip_opus = ?, format = ? WHERE id = ?`,
		st.Title, st.OwnerID, st.Target, st.Public, st.Audio, st.Holding, st.ClipAAC, st.ClipOpus, st.Format, st.ID)
	return affected(res, err)
}

// StreamPatch names the columns of a stream to change; a nil field leaves its column as it is. An OwnerID of 0
// clears the owner.
type StreamPatch struct {
	Title    *string
	OwnerID  *int64
	Target   *string
	Public   *bool
	Audio    *string
	Holding  *string
	ClipAAC  *string
	ClipOpus *string
	Format   *string
}

// PatchStream changes only the columns p names, so requests that change different parts of one stream at the same
// time each keep their change: a holding clip upload cannot put back the owner or the public flag it read before an
// admin changed them.
func (s *Store) PatchStream(ctx context.Context, id int64, p StreamPatch) error {
	var owner *int64
	if p.OwnerID != nil && *p.OwnerID != 0 {
		owner = p.OwnerID
	}
	res, err := s.db.ExecContext(ctx, `UPDATE streams SET title = coalesce(?, title),
		owner_id = CASE WHEN ? THEN ? ELSE owner_id END, target = coalesce(?, target), public = coalesce(?, public),
		audio = coalesce(?, audio), holding = coalesce(?, holding), clip_aac = coalesce(?, clip_aac),
		clip_opus = coalesce(?, clip_opus), format = coalesce(?, format) WHERE id = ?`,
		p.Title, p.OwnerID != nil, owner, p.Target, p.Public, p.Audio, p.Holding, p.ClipAAC, p.ClipOpus, p.Format, id)
	return affected(res, err)
}

// SetStreamKey records a stream's current key of a kind: its credential and encrypted secret.
func (s *Store) SetStreamKey(ctx context.Context, id int64, kind string, credentialID int64, enc string) error {
	_, err := s.ReplaceStreamKey(ctx, id, kind, credentialID, enc)
	return err
}

// ReplaceStreamKey makes a credential the stream's key of a kind, with its encrypted secret, and returns the key it
// replaced (nil if there was none), read in the same transaction: of concurrent replacements each gets back a
// different key, so the caller can revoke exactly the one it replaced and no key is left valid but unlinked.
func (s *Store) ReplaceStreamKey(ctx context.Context, id int64, kind string, credentialID int64, enc string) (*int64, error) {
	read, write := `SELECT publish_key_id FROM streams WHERE id = ?`, `UPDATE streams SET publish_key_id = ?, publish_key_enc = ? WHERE id = ?`
	if kind == KeyPlayback {
		read, write = `SELECT playback_key_id FROM streams WHERE id = ?`, `UPDATE streams SET playback_key_id = ?, playback_key_enc = ? WHERE id = ?`
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	var old *int64
	err = tx.QueryRowContext(ctx, read, id).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, write, credentialID, enc, id); err != nil {
		return nil, err
	}
	return old, tx.Commit()
}

// DeleteStream removes a stream record (its credentials stay, revoked by the caller, for the audit trail).
func (s *Store) DeleteStream(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM streams WHERE id = ?`, id)
	return affected(res, err)
}

// DeleteStreamKeys removes a stream record and, in the same transaction, revokes every credential it still points
// to that is valid: its two keys and its guest keys, however many. It returns their ids, for the caller to close what
// they opened. A key a concurrent request attached after the caller read the stream (a regenerated key, a new guest
// key) is among them, so nothing tied to the stream stays valid once its record and guest links are gone.
func (s *Store) DeleteStreamKeys(ctx context.Context, id int64) ([]int64, error) {
	now := ms(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	rows, err := tx.QueryContext(ctx, `UPDATE stream_credentials SET revoked_at = ?
		WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?) AND id IN (
			SELECT publish_key_id FROM streams WHERE id = ? UNION SELECT playback_key_id FROM streams WHERE id = ?
			UNION SELECT credential_id FROM stream_guest_keys WHERE stream_id = ?)
		RETURNING id`, now, now, id, id, id)
	if err != nil {
		return nil, err
	}
	var revoked []int64
	for rows.Next() {
		var c int64
		if err = rows.Scan(&c); err != nil {
			break
		}
		revoked = append(revoked, c)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM streams WHERE id = ?`, id)
	if err := affected(res, err); err != nil {
		return nil, err
	}
	return revoked, tx.Commit()
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Users lists every user by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, userSelect+` ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user; their sessions, layouts and join codes go with them, their streams lose their owner.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return affected(res, err)
}

// DeleteUserSessions signs a user out everywhere.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// CreateJoinCode stores the hash of a one-time join code for a user, replacing any earlier unused code.
func (s *Store) CreateJoinCode(ctx context.Context, userID int64, codeHash string, expires time.Time, by string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	if _, err := tx.ExecContext(ctx, `DELETE FROM join_codes WHERE user_id = ? AND used_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO join_codes (code_hash, user_id, expires_at, created_at, created_by)
		VALUES (?, ?, ?, ?, ?)`, codeHash, userID, ms(expires), ms(s.now()), by); err != nil {
		return err
	}
	return tx.Commit()
}

// RedeemJoinCode marks an unused, unexpired code used and sets the user's password hash, in one transaction. It
// returns the user, or ErrNotFound for an unknown, used or expired code.
func (s *Store) RedeemJoinCode(ctx context.Context, codeHash, passwordHash string) (User, error) {
	now := ms(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	var userID int64
	err = tx.QueryRowContext(ctx, `UPDATE join_codes SET used_at = ?
		WHERE code_hash = ? AND used_at IS NULL AND expires_at > ? RETURNING user_id`, now, codeHash, now).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, now, userID); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, userID)
}

// JoinCodeExpiry reports when a user's unused join code expires, if there is one.
func (s *Store) JoinCodeExpiry(ctx context.Context, userID int64) (*time.Time, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM join_codes WHERE user_id = ? AND used_at IS NULL
		ORDER BY expires_at DESC LIMIT 1`, userID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t := fromMS(at)
	return &t, nil
}

// JoinCodeUser returns the user an unused, unexpired join code belongs to, without using it up.
func (s *Store) JoinCodeUser(ctx context.Context, codeHash string) (User, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM join_codes WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`,
		codeHash, ms(s.now())).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, userID)
}

// EncoderAddress is an address that published to a stream over a port (a portgate port id).
type EncoderAddress struct {
	StreamID int64
	Port     string
	IP       string
	LastSeen time.Time
}

// maxEncoderAddresses is how many addresses a stream remembers; the least recently seen go first.
const maxEncoderAddresses = 3

// TouchEncoderAddress records that ip published to a stream over port at at, keeping the stream's newest three.
func (s *Store) TouchEncoderAddress(ctx context.Context, a EncoderAddress) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	if _, err := tx.ExecContext(ctx, `INSERT INTO encoder_addresses (stream_id, port, ip, last_seen) VALUES (?, ?, ?, ?)
		ON CONFLICT (stream_id, port, ip) DO UPDATE SET last_seen = excluded.last_seen`,
		a.StreamID, a.Port, a.IP, ms(a.LastSeen)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encoder_addresses WHERE stream_id = ? AND rowid NOT IN (
		SELECT rowid FROM encoder_addresses WHERE stream_id = ? ORDER BY last_seen DESC LIMIT ?)`,
		a.StreamID, a.StreamID, maxEncoderAddresses); err != nil {
		return err
	}
	return tx.Commit()
}

// EncoderAddresses lists the addresses seen since since, newest first.
func (s *Store) EncoderAddresses(ctx context.Context, since time.Time) ([]EncoderAddress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stream_id, port, ip, last_seen FROM encoder_addresses
		WHERE last_seen >= ? ORDER BY last_seen DESC`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EncoderAddress
	for rows.Next() {
		var a EncoderAddress
		var seen int64
		if err := rows.Scan(&a.StreamID, &a.Port, &a.IP, &seen); err != nil {
			return nil, err
		}
		a.LastSeen = fromMS(seen)
		out = append(out, a)
	}
	return out, rows.Err()
}
