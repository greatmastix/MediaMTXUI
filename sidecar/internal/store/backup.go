package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
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

// Inspect checks that this server can take a database file, and brings it up to this server's schema: intact, from
// this software (its triggers and views are this server's, word for word: nothing in it runs anything else), a
// schema no newer than this server's, set up, with an admin. Those checks read the file read-only. A file that
// passes them is then migrated in place, as the start after a restore would do (it is the restore's own copy, which
// that start opens as it is), and afterwards its triggers and views must be exactly this server's. So a database
// whose migrations fail is refused here and not at that start, and so is one without the audit log's append-only
// triggers (a migration recorded as applied never creates them again).
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
	_ = db.Close()
	migrated, err := migrateFile(ctx, path)
	if err != nil {
		return fail("this server's migrations fail on it (%v)", err)
	}
	for name, def := range ours {
		if migrated[name] != def {
			return fail("it lacks this server's %s", name)
		}
	}
	for name := range migrated {
		if _, ok := ours[name]; !ok {
			return fail("it has a trigger or view this server does not (%q)", name)
		}
	}
	return in, nil
}

// migrateFile runs this server's migrations on a database file the way Open does, and returns its triggers and views
// afterwards. Like Inspect's read, it lets nothing in the file's schema call a function that is not harmless.
func migrateFile(ctx context.Context, path string) (map[string]string, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=trusted_schema(0)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	return schemaObjects(ctx, db)
}

// CarryAudit appends the audit log of the database a restore replaced (the file at path, in state/pre-restore/) to
// this one's, leaving out the entries this one has already, word for word, and returns how many it added. A
// restored database brings the backup's log as it was when the backup was made; with this, a restore never takes
// an entry out of the log. What happened here up to the restore follows the backup's entries (under new ids), and
// an entry missing from or changed in the backup is there as it was. Running it again adds nothing.
func (s *Store) CarryAudit(ctx context.Context, path string) (int64, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err // ATTACH would create an empty database
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS replaced`, path); err != nil {
		return 0, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `DETACH DATABASE replaced`) //nolint:errcheck // nothing on the connection is still running
	res, err := conn.ExecContext(ctx, `INSERT INTO main.audit_log (at, actor, actor_user_id, ip, action, target, details)
		SELECT r.at, r.actor, r.actor_user_id, r.ip, r.action, r.target, r.details FROM replaced.audit_log r
		WHERE NOT EXISTS (SELECT 1 FROM main.audit_log m WHERE m.at = r.at AND m.actor = r.actor
			AND m.actor_user_id IS r.actor_user_id AND m.ip = r.ip AND m.action = r.action AND m.target = r.target
			AND m.details = r.details)
		ORDER BY r.id`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetMetas stores several values in the key-value table together: all of them, or on an error none (the backup
// key's two halves are useless apart).
func (s *Store) SetMetas(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit, a no-op
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, values[k]); err != nil {
			return err
		}
	}
	return tx.Commit()
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
