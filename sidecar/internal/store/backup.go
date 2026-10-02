package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Backups: a consistent copy of the database, and the checks a copy passes before it is restored.

// Snapshot writes a consistent, compact copy of the database to path, which must not exist (VACUUM INTO: one read
// transaction, so the server keeps writing meanwhile).
func (s *Store) Snapshot(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// SchemaVersion is the newest migration applied.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	return schemaVersion(ctx, s.db)
}

func schemaVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&v)
	return v.Int64, err
}

// ClearSessions ends every session (after a restore, everyone signs in again).
func (s *Store) ClearSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions`)
	return err
}

// Inspection is what a database file holds, read before it is restored.
type Inspection struct {
	Schema         int64    `json:"schema"`
	Users          []string `json:"users"`
	Admins         int      `json:"admins"`
	Streams        []string `json:"streams"`
	Credentials    int      `json:"credentials"`
	Snapshots      int      `json:"snapshots"`
	HistoryMinutes int      `json:"historyMinutes"`
	AuditEntries   int      `json:"auditEntries"`
}

// ErrNotRestorable explains why a database file cannot be restored here.
var ErrNotRestorable = errors.New("the backup's database cannot be restored here")

// Inspect opens a database file read-only and checks that this server can take it: intact, from this software (its
// triggers and views are this server's, word for word: nothing in it runs anything else), a schema no newer than
// this server's, set up, with an admin.
func (s *Store) Inspect(ctx context.Context, path string) (Inspection, error) {
	var in Inspection
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=trusted_schema(0)")
	if err != nil {
		return in, err
	}
	defer db.Close()
	fail := func(format string, args ...any) (Inspection, error) {
		return in, fmt.Errorf("%w: %s", ErrNotRestorable, fmt.Sprintf(format, args...))
	}
	var check string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		return fail("it is not an intact SQLite database")
	}
	ours, err := schemaObjects(ctx, s.db)
	if err != nil {
		return in, err
	}
	theirs, err := schemaObjects(ctx, db)
	if err != nil {
		return fail("its schema cannot be read")
	}
	for name, def := range theirs {
		if ours[name] != def {
			return fail("it has a trigger or view this server does not (%q)", name)
		}
	}
	if in.Schema, err = schemaVersion(ctx, db); err != nil || in.Schema < 1 {
		return fail("it is not a MediaMTX UI database")
	}
	mine, err := s.SchemaVersion(ctx)
	if err != nil {
		return in, err
	}
	if in.Schema > mine {
		return fail("it comes from a newer version of the sidecar (schema %d, this one has %d); update first", in.Schema, mine)
	}
	var setup int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key = ?`, metaSetupCompleted).Scan(&setup); err != nil || setup == 0 {
		return fail("its server was never set up")
	}
	if in.Users, err = queryStrings(ctx, db, `SELECT username FROM users ORDER BY username`); err != nil {
		return fail("its users cannot be read")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&in.Admins); err != nil || in.Admins == 0 {
		return fail("it has no admin")
	}
	if in.Schema >= 4 {
		if in.Streams, err = queryStrings(ctx, db, `SELECT name FROM streams ORDER BY name`); err != nil {
			return fail("its streams cannot be read")
		}
	}
	counts := []struct {
		dst   *int
		table string
		since int64
	}{{&in.Credentials, "stream_credentials", 1}, {&in.Snapshots, "config_snapshots", 1}, {&in.AuditEntries, "audit_log", 1}, {&in.HistoryMinutes, "history_minutes", 13}}
	for _, c := range counts {
		if in.Schema < c.since {
			continue
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+c.table).Scan(c.dst); err != nil {
			return fail("%s cannot be read", c.table)
		}
	}
	return in, nil
}

func schemaObjects(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type || ' ' || name, coalesce(sql, '') FROM sqlite_master WHERE type IN ('trigger', 'view')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			return nil, err
		}
		out[name] = def
	}
	return out, rows.Err()
}

func queryStrings(ctx context.Context, db *sql.DB, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
