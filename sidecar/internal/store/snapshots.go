package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// Snapshot is a mediamtx.yml the sidecar wrote or adopted.
type Snapshot struct {
	ID       int64
	At       time.Time
	SHA256   string
	Content  []byte
	Author   string
	Reason   string
	ParentID *int64
}

// InsertSnapshot stores content as the newest snapshot, parented to the previous one.
func (s *Store) InsertSnapshot(ctx context.Context, content []byte, author, reason string) (Snapshot, error) {
	sum := sha256.Sum256(content)
	snap := Snapshot{At: s.now(), SHA256: hex.EncodeToString(sum[:]), Content: content, Author: author, Reason: reason}
	if prev, err := s.LatestSnapshot(ctx); err == nil {
		snap.ParentID = &prev.ID
	} else if !errors.Is(err, ErrNotFound) {
		return Snapshot{}, err
	}
	var parent sql.NullInt64
	if snap.ParentID != nil {
		parent = sql.NullInt64{Int64: *snap.ParentID, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO config_snapshots (at, sha256, content, author, reason, parent_id)
		VALUES (?, ?, ?, ?, ?, ?)`, ms(snap.At), snap.SHA256, content, author, reason, parent)
	if err != nil {
		return Snapshot{}, err
	}
	snap.ID, err = res.LastInsertId()
	return snap, err
}

// LatestSnapshot returns the newest snapshot, or ErrNotFound.
func (s *Store) LatestSnapshot(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	var at int64
	var parent sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, at, sha256, content, author, reason, parent_id
		FROM config_snapshots ORDER BY id DESC LIMIT 1`).
		Scan(&snap.ID, &at, &snap.SHA256, &snap.Content, &snap.Author, &snap.Reason, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	snap.At = fromMS(at)
	if parent.Valid {
		snap.ParentID = &parent.Int64
	}
	return snap, nil
}

// ListSnapshots returns up to limit snapshots older than beforeID (all when beforeID is 0), newest first, without
// their content.
func (s *Store) ListSnapshots(ctx context.Context, beforeID int64, limit int) ([]Snapshot, error) {
	if beforeID <= 0 {
		beforeID = 1<<63 - 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, sha256, author, reason, parent_id FROM config_snapshots
		WHERE id < ? ORDER BY id DESC LIMIT ?`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		var snap Snapshot
		var at int64
		var parent sql.NullInt64
		if err := rows.Scan(&snap.ID, &at, &snap.SHA256, &snap.Author, &snap.Reason, &parent); err != nil {
			return nil, err
		}
		snap.At = fromMS(at)
		if parent.Valid {
			snap.ParentID = &parent.Int64
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// SnapshotByID returns one snapshot with its content, or ErrNotFound.
func (s *Store) SnapshotByID(ctx context.Context, id int64) (Snapshot, error) {
	var snap Snapshot
	var at int64
	var parent sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, at, sha256, content, author, reason, parent_id
		FROM config_snapshots WHERE id = ?`, id).
		Scan(&snap.ID, &at, &snap.SHA256, &snap.Content, &snap.Author, &snap.Reason, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	snap.At = fromMS(at)
	if parent.Valid {
		snap.ParentID = &parent.Int64
	}
	return snap, nil
}
