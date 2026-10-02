package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Credential is a stream credential: what a publisher or reader presents to MediaMTX.
type Credential struct {
	ID          int64
	Name        string   // the username clients present (kind password), or a label (kind token)
	Kind        string   // "password" or "token"
	SecretHash  string   // HMAC of the secret, hex
	Actions     []string // publish, read, playback, metrics
	Paths       []string // path names, or ~regex; empty means any path
	SourceCIDRs []string // empty means any source
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
	CreatedBy   string
	LastUsedAt  *time.Time
}

// ErrExists is returned when a unique name or secret is already taken.
var ErrExists = errors.New("already exists")

// CreateCredential stores a credential and returns its id.
func (s *Store) CreateCredential(ctx context.Context, c Credential) (int64, error) {
	actions, _ := json.Marshal(nonNil(c.Actions))
	paths, _ := json.Marshal(nonNil(c.Paths))
	cidrs, _ := json.Marshal(nonNil(c.SourceCIDRs))
	res, err := s.db.ExecContext(ctx, `INSERT INTO stream_credentials
		(name, kind, secret_hash, actions, paths, source_cidrs, expires_at, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Kind, c.SecretHash, string(actions), string(paths), string(cidrs), nullMS(c.ExpiresAt), ms(c.CreatedAt), c.CreatedBy)
	if isUniqueViolation(err) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CredentialByName looks a credential up by name.
func (s *Store) CredentialByName(ctx context.Context, name string) (Credential, error) {
	return scanCredential(s.db.QueryRowContext(ctx, credentialSelect+` WHERE name = ?`, name))
}

// CredentialBySecretHash looks a credential up by the HMAC of its secret (bearer tokens).
func (s *Store) CredentialBySecretHash(ctx context.Context, hash string) (Credential, error) {
	return scanCredential(s.db.QueryRowContext(ctx, credentialSelect+` WHERE secret_hash = ?`, hash))
}

// ListCredentials returns every credential, revoked ones included, by name.
func (s *Store) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, credentialSelect+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeCredential marks a credential revoked. Revoking twice keeps the first time.
func (s *Store) RevokeCredential(ctx context.Context, name string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE stream_credentials SET revoked_at = coalesce(revoked_at, ?) WHERE name = ?`, ms(at), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkCredentialUsed records the last successful use.
func (s *Store) MarkCredentialUsed(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stream_credentials SET last_used_at = ? WHERE id = ?`, ms(at), id)
	return err
}

//nolint:gosec // a column list, not a credential
const credentialSelect = `SELECT id, name, kind, secret_hash, actions, paths, source_cidrs, expires_at, revoked_at,
	created_at, created_by, last_used_at FROM stream_credentials`

type scanner interface{ Scan(dest ...any) error }

func scanCredential(row scanner) (Credential, error) {
	var c Credential
	var actions, paths, cidrs string
	var expires, revoked, used sql.NullInt64
	var created int64
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.SecretHash, &actions, &paths, &cidrs, &expires, &revoked, &created, &c.CreatedBy, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, err
	}
	for _, f := range []struct {
		src string
		dst *[]string
	}{{actions, &c.Actions}, {paths, &c.Paths}, {cidrs, &c.SourceCIDRs}} {
		if err := json.Unmarshal([]byte(f.src), f.dst); err != nil {
			return Credential{}, err
		}
	}
	c.ExpiresAt, c.RevokedAt, c.LastUsedAt = fromNullMS(expires), fromNullMS(revoked), fromNullMS(used)
	c.CreatedAt = fromMS(created)
	return c, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
