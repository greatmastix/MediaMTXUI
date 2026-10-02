package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/pressly/goose/v3"
)

// Migrations written in Go, next to the SQL files in migrations/. They exist where SQL alone cannot be safe.

// goMigrations are registered with the goose provider in version order with the SQL files.
func goMigrations() []*goose.Migration {
	return []*goose.Migration{
		goose.NewGoMigration(3, &goose.GoFunc{RunDB: usersAllowStreamer}, nil),
	}
}

// usersAllowStreamer widens users.role's CHECK to include "streamer". SQLite cannot alter a CHECK, so
// the table is rebuilt the documented way (sqlite.org/lang_altertable.html, "other kinds of table schema changes"):
// on one connection, with foreign keys off, inside a transaction, checked with foreign_key_check before commit.
// Through the pool, a DROP TABLE users with foreign keys on would cascade into every session and layout.
func usersAllowStreamer(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		if _, e := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`); e != nil && err == nil {
			err = e
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, stmt := range []string{
		`CREATE TABLE users_new (
		  id            INTEGER PRIMARY KEY,
		  username      TEXT NOT NULL COLLATE NOCASE UNIQUE,
		  password_hash TEXT NOT NULL,
		  role          TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer', 'streamer')),
		  disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
		  created_at    INTEGER NOT NULL,
		  updated_at    INTEGER NOT NULL
		) STRICT`,
		`INSERT INTO users_new (id, username, password_hash, role, disabled, created_at, updated_at)
		   SELECT id, username, password_hash, role, disabled, created_at, updated_at FROM users`,
		`DROP TABLE users`,
		`ALTER TABLE users_new RENAME TO users`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuilding users: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	broken := rows.Next()
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if broken {
		return errors.New("rebuilding users: foreign_key_check found broken references")
	}
	return tx.Commit()
}
