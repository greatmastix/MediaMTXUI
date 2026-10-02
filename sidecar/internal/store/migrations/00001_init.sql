-- +goose Up
-- Times are Unix milliseconds. Migrations are forward-only.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL COLLATE NOCASE UNIQUE,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
  disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
) STRICT;

-- id_hash is the SHA-256 of the session token; the token itself is only ever in the cookie.
CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  csrf_token   TEXT NOT NULL,
  auth_method  TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  ip           TEXT NOT NULL,
  user_agent   TEXT NOT NULL
) STRICT;
CREATE INDEX sessions_user_id ON sessions (user_id);

-- Credentials MediaMTX clients present (publishers, readers). secret_hash is an HMAC under the key in state/.
-- actions, paths and source_cidrs are JSON arrays; empty paths or source_cidrs mean any.
CREATE TABLE stream_credentials (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  kind         TEXT NOT NULL CHECK (kind IN ('password', 'token')),
  secret_hash  TEXT NOT NULL UNIQUE,
  actions      TEXT NOT NULL,
  paths        TEXT NOT NULL,
  source_cidrs TEXT NOT NULL,
  expires_at   INTEGER,
  revoked_at   INTEGER,
  created_at   INTEGER NOT NULL,
  created_by   TEXT NOT NULL,
  last_used_at INTEGER
) STRICT;

-- Append-only: the triggers below refuse updates and deletes. actor_user_id has no foreign key on purpose,
-- so entries outlive deleted users.
CREATE TABLE audit_log (
  id            INTEGER PRIMARY KEY,
  at            INTEGER NOT NULL,
  actor         TEXT NOT NULL,
  actor_user_id INTEGER,
  ip            TEXT NOT NULL,
  action        TEXT NOT NULL,
  target        TEXT NOT NULL,
  details       TEXT NOT NULL
) STRICT;
CREATE INDEX audit_log_at ON audit_log (at);

-- +goose StatementBegin
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
  SELECT RAISE(ABORT, 'audit_log is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
  SELECT RAISE(ABORT, 'audit_log is append-only');
END;
-- +goose StatementEnd

-- Every mediamtx.yml the sidecar writes or adopts.
CREATE TABLE config_snapshots (
  id        INTEGER PRIMARY KEY,
  at        INTEGER NOT NULL,
  sha256    TEXT NOT NULL,
  content   BLOB NOT NULL,
  author    TEXT NOT NULL,
  reason    TEXT NOT NULL,
  parent_id INTEGER REFERENCES config_snapshots (id)
) STRICT;
